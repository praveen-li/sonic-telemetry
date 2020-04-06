package main

// bgp_metrics_test covers test functions of bgp metrics daemon,  parses show bgp vty commands,
// writes BGP metrics to redis DB STATE_DB, verify with gnmi get for the BGP_TABLE
// Prerequisite: redis-server should be running.

import (
	"crypto/tls"
	"encoding/json"
	testcert "github.com/Azure/sonic-telemetry/testdata/tls"
	"github.com/go-redis/redis"
	"github.com/golang/protobuf/proto"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"github.com/openconfig/gnmi/value"
	"golang.org/x/net/context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"io/ioutil"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
	spb "github.com/Azure/sonic-telemetry/proto"
	sdc "github.com/Azure/sonic-telemetry/sonic_data_client"
	gclient "github.com/jipanyang/gnmi/client/gnmi"
        svr "github.com/Azure/sonic-telemetry/gnmi_server"
)

var clientTypes = []string{gclient.Type}

func createServer(t *testing.T) *svr.Server {
	certificate, err := testcert.NewCert()
	if err != nil {
		t.Errorf("could not load server key pair: %s", err)
	}
	tlsCfg := &tls.Config{
		ClientAuth:   tls.RequestClientCert,
		Certificates: []tls.Certificate{certificate},
	}

	opts := []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tlsCfg))}
	cfg := &svr.Config{Port: 8081}
	s, err := svr.NewServer(cfg, opts)
	if err != nil {
		t.Errorf("Failed to create gNMI server: %v", err)
	}
	return s
}

// runTestGet requests a path from the server by Get grpc call, and compares if
// the return code and response value are expected.
func runTestGet(t *testing.T, ctx context.Context, gClient pb.GNMIClient, pathTarget string,
	textPbPath string, wantRetCode codes.Code, wantRespVal interface{}) {

	// Send request
	var pbPath pb.Path
	if err := proto.UnmarshalText(textPbPath, &pbPath); err != nil {
		t.Fatalf("error in unmarshaling path: %v %v", textPbPath, err)
	}
	prefix := pb.Path{Target: pathTarget}
	req := &pb.GetRequest{
		Prefix:   &prefix,
		Path:     []*pb.Path{&pbPath},
		Encoding: pb.Encoding_JSON_IETF,
	}

	resp, err := gClient.Get(ctx, req)
	// Check return code
	gotRetStatus, ok := status.FromError(err)
	if !ok {
		t.Fatal("got a non-grpc error from grpc call")
	}
	if gotRetStatus.Code() != wantRetCode {
		t.Log("err: ", err)
		t.Fatalf("got return code %v, want %v", gotRetStatus.Code(), wantRetCode)
	}

	// Check response value
	var gotVal interface{}
	if resp != nil {
		notifs := resp.GetNotification()
		if len(notifs) != 1 {
			t.Fatalf("got %d notifications, want 1", len(notifs))
		}
		updates := notifs[0].GetUpdate()
		if len(updates) != 1 {
			t.Fatalf("got %d updates in the notification, want 1", len(updates))
		}
		val := updates[0].GetVal()
		if val.GetJsonIetfVal() == nil {
			gotVal, err = value.ToScalar(val)
			if err != nil {
				t.Errorf("got: %v, want a scalar value", gotVal)
			}
		} else {
			// Unmarshal json data to gotVal container for comparison
			if err := json.Unmarshal(val.GetJsonIetfVal(), &gotVal); err != nil {
				t.Fatalf("error in unmarshaling IETF JSON data to json container: %v", err)
			}
			var wantJSONStruct interface{}
			if err := json.Unmarshal(wantRespVal.([]byte), &wantJSONStruct); err != nil {
				t.Fatalf("error in unmarshaling IETF JSON data to json container: %v", err)
			}
			wantRespVal = wantJSONStruct
		}
	}

	if !reflect.DeepEqual(gotVal, wantRespVal) {
		t.Errorf("got: %v (%T),\nwant %v (%T)", gotVal, gotVal, wantRespVal, wantRespVal)
	}
}

func runServer(t *testing.T, s *svr.Server) {
	err := s.Serve() // blocks until close
	if err != nil {
		t.Fatalf("gRPC server err: %v", err)
	}
}

// Redis client for STATE_DB
func getRedisClient(t *testing.T) *redis.Client {
	dbn := spb.Target_value["STATE_DB"]
	rclient := redis.NewClient(&redis.Options{
		Network:     "tcp",
		Addr:        "localhost:6379",
		Password:    "",
		DB:          int(dbn),
		DialTimeout: 0,
	})
	_, err := rclient.Ping().Result()
	if err != nil {
		t.Fatalf("failed to connect to redis server %v", err)
	}
	return rclient
}

// Parse BGP vty commands, write to REDIS DB
func prepareDb(t *testing.T, fileName string) {
	rclient := getRedisClient(t)
	defer rclient.Close()
	rclient.FlushDb()
	//Enable keysapce notification
	os.Setenv("PATH", "/usr/bin:/sbin:/bin:/usr/local/bin")
	cmd := exec.Command("redis-cli", "config", "set", "notify-keyspace-events", "KEA")
	_, err := cmd.Output()
	if err != nil {
		t.Fatal("failed to enable redis keyspace notification ", err)
	}

	bytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		t.Fatalf("read file %v err: %v bytes: %s", fileName, err, string(bytes))
	}

        poller := &BgpPoller{}
        poller.BgpData = make(map[string]BgpMetrics)
        poller.RedisDbConn.redisClient = rclient

        m := BGPSummaryMessage{}
        m.unmarshalMessage(bytes, poller, "")
        poller.Write_redisDb()
}

// Test BGP metrics in Redis DB
func TestBGPMetricsSum(t *testing.T) {
	s := createServer(t)
	go runServer(t, s)

        fileName := "../testdata/BGP_summary.txt"
	prepareDb(t, fileName)

	tlsConfig := &tls.Config{InsecureSkipVerify: true}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))}

	targetAddr := "127.0.0.1:8081"
	conn, err := grpc.Dial(targetAddr, opts...)
	if err != nil {
		t.Fatalf("Dialing to %q failed: %v", targetAddr, err)
	}
	defer conn.Close()

	gClient := pb.NewGNMIClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fileName = "../testdata/BGPMetrics_ipv4_peer1.txt"
	BGPIpv4Peer1Bytes, err := ioutil.ReadFile(fileName)
	if err != nil {
		t.Fatalf("read file %v err: %v", fileName, err)
	}

        fileName = "../testdata/BGPMetrics_ipv4_peer2.txt"
        BGPIpv4Peer2Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        fileName = "../testdata/BGPMetrics_ipv6_peer1.txt"
        BGPIpv6Peer1Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        fileName = "../testdata/BGPMetrics_ipv6_peer2.txt"
        BGPIpv6Peer2Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

	tds := []struct {
		desc        string
		pathTarget  string
		textPbPath  string
		wantRetCode codes.Code
		wantRespVal interface{}
	}{{
		desc:       "get BGP_TABLE:10.2.1.1",
		pathTarget: "STATE_DB",
		textPbPath: `
					elem: <name: "BGP_TABLE" >
					elem: <name: "10.2.1.1" >
				`,
		wantRetCode: codes.OK,
		wantRespVal: BGPIpv4Peer1Bytes,
	}, {
                desc:       "get BGP_TABLE:10.2.2.1",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "10.2.2.1" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv4Peer2Bytes,
        }, {
                desc:       "get BGP_TABLE:2000:2:1::2",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "2000:2:1::2" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv6Peer1Bytes,
        }, {
                desc:       "get BGP_TABLE:2000:2:2::2",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "2000:2:2::2" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv6Peer2Bytes,
        }}

	for _, td := range tds {
		t.Run(td.desc, func(t *testing.T) {
			runTestGet(t, ctx, gClient, td.pathTarget, td.textPbPath, td.wantRetCode, td.wantRespVal)
		})
	}

        s.Stop()
}

func TestBGPMetricsSumIpv4(t *testing.T) {
        s := createServer(t)
        go runServer(t, s)

        fileName := "../testdata/BGP_summary_ipv4.txt"
        prepareDb(t, fileName)

        tlsConfig := &tls.Config{InsecureSkipVerify: true}
        opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))}

        targetAddr := "127.0.0.1:8081"
        conn, err := grpc.Dial(targetAddr, opts...)
        if err != nil {
                t.Fatalf("Dialing to %q failed: %v", targetAddr, err)
        }
        defer conn.Close()

        gClient := pb.NewGNMIClient(conn)
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        fileName = "../testdata/BGPMetrics_ipv4_peer1.txt"
        BGPIpv4Peer1Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        fileName = "../testdata/BGPMetrics_ipv4_peer2.txt"
        BGPIpv4Peer2Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        tds := []struct {
                desc        string
                pathTarget  string
                textPbPath  string
                wantRetCode codes.Code
                wantRespVal interface{}
        }{{
                desc:       "get BGP_TABLE:10.2.1.1",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "10.2.1.1" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv4Peer1Bytes,
        }, {
                desc:       "get BGP_TABLE:10.2.2.1",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "10.2.2.1" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv4Peer2Bytes,
        }}

        for _, td := range tds {
                t.Run(td.desc, func(t *testing.T) {
                        runTestGet(t, ctx, gClient, td.pathTarget, td.textPbPath, td.wantRetCode, td.wantRespVal)
                })
        }

        s.Stop()
}

func TestBGPMetricsSumIpv6(t *testing.T) {
        s := createServer(t)
        go runServer(t, s)

        fileName := "../testdata/BGP_summary_ipv6.txt"
        prepareDb(t, fileName)

        tlsConfig := &tls.Config{InsecureSkipVerify: true}
        opts := []grpc.DialOption{grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))}

        targetAddr := "127.0.0.1:8081"
        conn, err := grpc.Dial(targetAddr, opts...)
        if err != nil {
                t.Fatalf("Dialing to %q failed: %v", targetAddr, err)
        }
        defer conn.Close()

        gClient := pb.NewGNMIClient(conn)
        ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()

        fileName = "../testdata/BGPMetrics_ipv6_peer1.txt"
        BGPIpv6Peer1Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        fileName = "../testdata/BGPMetrics_ipv6_peer2.txt"
        BGPIpv6Peer2Bytes, err := ioutil.ReadFile(fileName)
        if err != nil {
                t.Fatalf("read file %v err: %v", fileName, err)
        }

        tds := []struct {
                desc        string
                pathTarget  string
                textPbPath  string
                wantRetCode codes.Code
                wantRespVal interface{}
        }{{
                desc:       "get BGP_TABLE:2000:2:1::2",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "2000:2:1::2" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv6Peer1Bytes,
        }, {
                desc:       "get BGP_TABLE:2000:2:2::2",
                pathTarget: "STATE_DB",
                textPbPath: `
                                        elem: <name: "BGP_TABLE" >
                                        elem: <name: "2000:2:2::2" >
                                `,
                wantRetCode: codes.OK,
                wantRespVal: BGPIpv6Peer2Bytes,
        }}

        for _, td := range tds {
                t.Run(td.desc, func(t *testing.T) {
                        runTestGet(t, ctx, gClient, td.pathTarget, td.textPbPath, td.wantRetCode, td.wantRespVal)
                })
        }

        s.Stop()
}

func init() {
	// Inform gNMI server to use redis tcp localhost connection
	sdc.UseRedisLocalTcpPort = true
}
