package main

import (
    "fmt"
    "net"
    "strings"
    "time"
    "reflect"
    "io"
    "os"
    "os/signal"
    "context"
    "flag"

    "encoding/json"
    log "github.com/golang/glog"
    spb "github.com/Azure/sonic-telemetry/proto"
    "github.com/go-redis/redis"
)

const (
        maxBufferSize   int = 65535                             // max buf size for vty response
        passwd          = "zebra\n"                             // BGP vty password
        showBgpSum      = "show bgp summary json\n"             // show bgp summary vty command
        showBgpNeigh    = "show bgp neighbor "                  // show bgp neighbor vty command
        dbTable         = "BGP_TABLE|"                          // BGP table name in REDIS DB
        db_others       = "OTHERS"                              // gNMI target: OTHERS
        adminDown       = 1                                     // AdminStatus(1): Down
        adminUp         = 2                                     // AdminStatus(2): Up
)

var (
        bgpPollerCfg = BGPPollerConfig {
                RedisLocalTcpPort:  "localhost:6379",
                TCPPort:            "localhost:2605",
                PollingInterval:    10 * time.Second,
                DialTimeOut:        10 * time.Second,
                RedisDialTimeOut:   10 * time.Second,
                TargetDB:          "STATE_DB",
        }
)

// BGPPollerConfig defines data structure for the configuration of poller
type BGPPollerConfig struct {
    PollingInterval       time.Duration          // BGP metrics polling interval
    DialTimeOut           time.Duration          // BGP docker TCP conn dial timeout
    RedisDialTimeOut      time.Duration          // RedisDB conn dial timeout
    RedisLocalTcpPort     string                 // Redis local address and port
    TCPPort               string                 // BGP VTY local address and port
    TargetDB              string                 // Redis DBs to connect to
}

// BgpPoller defines data structure for BGP metrics poller
type BgpPoller struct {
    BgpConn        BgpTcpConn             // tcp connection to BGP docker
    RedisDbConn    RedisConn              // Redis DB connections
    BgpData        map[string]BgpMetrics  // BGP metrics for all neighbors to be written to RedisDb
    Handlers       map[string]Command     // handler for vty commands
    SummaryCmdOnly bool                   // if show summary cmd has the peer metrics
}

type RedisConn struct {
    redisClient     *redis.Client
}

type BgpTcpConn struct {
    PollingInterval   time.Duration
    Socket            net.Conn
}

// interface define the interface for vty commands handling
type Command interface {
    unmarshalMessage(message []byte, poller* BgpPoller, neighbor string) error
    run_command(poller* BgpPoller, cmd string)
}

// BgpMetrics defines structure for BGP metrics written to Redis DB
type BgpMetrics struct {
    BgpPeerDescription            string
    BgpPeerAdminStatus            int
    BgpPeerState                  string
    BgpPeerLocalAddr              string
    BgpPeerEstablishedTime        int
    BgpPeerInTotalMessages        int
    BgpPrefixInPrefixesAccepted   int
    BgpPrefixOutPrefixes          int
    BgpPeerEstablishedTransitions int
}

// Response messages for show bgp summary
type BGPSummaryMessage struct {
    Ipv4Unicast json.RawMessage
    Ipv6Unicast json.RawMessage
}

// Response messages for show bgp neighbor
type BGPNeighborMessage struct {
    Neighbor    map[string]BGPPeerMessage
}

type MultiPathRelax struct {
     multiPathRelax string
}

// Peers structure defines the Peers from vty response
type Peers struct {
   RemoteAs                    int        `json:"remoteAs"`
   Version                     int        `json:"version"`
   MsgRcvd                     int        `json:"msgRcvd"`
   MsgSent                     int        `json:"msgSent"`
   TableVersion                int        `json:"tableVersion"`
   Outq                        int        `json:"outq"`
   Inq                         int        `json:"inq"`
   PeerUptime                  string     `json:"peerUptime"`
   PeerUptimeMsec              int        `json:"peerUptimeMsec"`
   PeerUptimeEstablishedEpoch  int        `json:"peerUptimeEstablishedEpoch"`
   PrefixReceivedCount         int        `json:"prefixReceivedCount"`
   State                       string     `json:"state"`
   IdType                      string     `json:"idType"`
   PeerDescription             *string    `json:"description,omitempty"`
   PrefixOutCount              *int       `json:"advertisedPrefixes,omitempty"`
   EstablishedTransitions      int        `json:"connectionsEstablished,omitempty"`
}

// BgpUnicast structure defines the unicast fields from vty response
type BgpUnicast struct {
    RouterId             string             `json:"routerId"`
    As                   int                `json:"as"`
    VrfId                int                `json:"vrfId"`
    VrfName              string             `json:"vrfName"`
    TableVersion         int                `json:"tableVersion"`
    RibCount             int                `json:"ribCount"`
    RibMemory            int                `json:"ribMemory"`
    PeerCount            int                `json:"peerCount"`
    PeerMemory           int                `json:"peerMemory"`
    PeerGroupCount       int                `json:"peerGroupCount"`
    PeerGroupMemory      int                `json:"peerGroupMemory"`
    Peers                map[string]Peers   `json:"peers"`
    TotalPeers           int                `json:"totalPeers"`
    DynamicPeers         int                `json:"dynamicPeers"`
    BestPath             MultiPathRelax     `json:"bestPath"`
}

// Response messages for BGP neighbor commands
type BGPPeerMessage struct {
    DataOther           json.RawMessage
    PeerDescription     string     `json:"nbrDesc"`
    PeerState           string     `json:"bgpState"`
    PeerUpTime          int        `json:"bgpTimerUp"`
}

// trim substring
func TrimSuffix(value string, a string) (string, error) {
    pos := strings.LastIndex(value, a)

    if pos == -1 {
        return value, fmt.Errorf("TrimSuffix substring: %s not found", a)
    }

    return value[:pos+1], nil
}

func (m BGPSummaryMessage) unicastPeers(peers []byte, poller *BgpPoller) error {
    var cm BgpUnicast

    if len(peers) == 0 {
        return nil
    }

    if err := json.Unmarshal(peers, &cm); err != nil {
         if e, ok := err.(*json.SyntaxError); ok {
            log.V(2).Infof("BGP summary unicastPeers unmarshal syntax error at byte offset %d\n", e.Offset)
        }
        return fmt.Errorf("BGPSummaryMessage unicastPeers Unmarshal error: %v", err)
    }

    // save the ipv4/ipv6 peer's info to metrics map
    for k, v := range cm.Peers {
        value := poller.BgpData[k]
        value.BgpPrefixInPrefixesAccepted = v.PrefixReceivedCount
        value.BgpPeerEstablishedTime  = v.PeerUptimeEstablishedEpoch
        value.BgpPeerInTotalMessages  = v.MsgSent
        value.BgpPeerLocalAddr = k
        value.BgpPeerState = v.State
        value.BgpPeerAdminStatus = adminUp
        value.BgpPeerEstablishedTransitions = v.EstablishedTransitions

        if (v.State == "Idle (Admin)") {
            value.BgpPeerAdminStatus = adminDown
        }

        if v.PrefixOutCount != nil {
            poller.SummaryCmdOnly = true
            value.BgpPrefixOutPrefixes = *v.PrefixOutCount
        } else {
            // Not supported if not in show summary command
            value.BgpPrefixOutPrefixes = -1
        }

        if v.PeerDescription != nil {
            value.BgpPeerDescription = *v.PeerDescription
        }

        poller.BgpData[k] = value
    }
    return nil
}

func (m BGPSummaryMessage) unmarshalMessage(message []byte, poller *BgpPoller, neighbor string) error {
    if err := json.Unmarshal(message, &m); err != nil {
        if e, ok := err.(*json.SyntaxError); ok {
            log.V(2).Infof("syntax error at byte offset %d", e.Offset)
        }
        return fmt.Errorf("BGPSummaryMessage unmarshalMessage error: %v", err)
    }

    // Ipv4Unicast
    if err := m.unicastPeers([]byte(m.Ipv4Unicast), poller); err != nil {
        return fmt.Errorf("BGPSummaryMessage Ipv4 unmarshal error: %v", err)
    }

    // Ipv6Unicast
    if err := m.unicastPeers([]byte(m.Ipv6Unicast), poller); err != nil {
        return fmt.Errorf("BGPSummaryMessage Ipv6 unicast unmarshal error: %v", err)
    }

    return nil
}

func (m BGPSummaryMessage) run_command(poller *BgpPoller, cmd_key string) {
    // run bgp show summmary and get all neighbors
    cmd := showBgpSum

    if poller.BgpConn.Socket == nil {
        if err := poller.bgpConn(); err != nil {
            log.V(2).Infof("BGP connect error: %v", err)
            return
        }
    }

    log.V(5).Infof("Send show bgp summary json command")
    // send vty command
    _, err := poller.BgpConn.Socket.Write([]byte(cmd))
    if err != nil {
         log.V(2).Infof("BGPSummaryMessage socket write error: %v", err)
         // reconnect to BGP docker
         if err := poller.bgpReConn(); err != nil {
             log.V(2).Infof("BGPSummaryMessage socket reconnect error: %v", err)
             return
         }

         poller.BgpConn.Socket.Write([]byte(cmd))
    }

    handler := poller.Handlers[cmd_key]

    // process the response message
    poller.process_response("", handler)
}

func (m BGPNeighborMessage) unmarshalMessage(message []byte, poller *BgpPoller, neighbor string) error {
    if err := json.Unmarshal(message, &m.Neighbor); err != nil {
        if e, ok := err.(*json.SyntaxError); ok {
            return fmt.Errorf("BGPNeighborMessage unmarshalMessage error at byte offset %d", e.Offset)
        }
    }

    for k, v := range m.Neighbor {
       log.V(5).Infof("k: %s Neighbor: %s ", k, v.PeerDescription)
    }

    // Save the neighbor info into metrics map
    if v, ok := m.Neighbor[neighbor]; ok {
        if value, ok := poller.BgpData[neighbor]; ok {
            value.BgpPeerDescription = v.PeerDescription
            poller.BgpData[neighbor] = value
        }
    } else {
        log.V(2).Infof("Neighbor: %s not found in summary command", neighbor)
    }
    return nil
}

func (m BGPNeighborMessage) run_command(poller *BgpPoller, cmd_str string) {
    log.V(5).Infof("Send show bgp neighbor json command")

    // bgp show summary command has neighbor metrics
    if poller.SummaryCmdOnly == true {
           return
    }

    if poller.BgpConn.Socket == nil {
        if err := poller.bgpConn(); err != nil {
            log.V(2).Infof("BGP connect error: %v", err)
            return
        }
    }

    // for each neighbor
    for k, _ := range poller.BgpData {
        // show bgp neighbors <neighbor> json
        cmd  := showBgpNeigh + k + " json\n"

        handler := poller.Handlers[cmd_str]

        // send vty command
        _, err := poller.BgpConn.Socket.Write([]byte(cmd))
        if err != nil {
            log.V(2).Infof("BGPNeighborMessage socket write error: %v", err)
            // reconnect to BGP docker
            if err := poller.bgpReConn(); err != nil {
                log.V(2).Infof("BGPNeighborMessage socket reconnect error: %v", err)
                return
            }

            poller.BgpConn.Socket.Write([]byte(cmd))
        }

        // process response
        poller.process_response(k, handler)
    }
}

func (poller *BgpPoller) login() error {
    buf := make([]byte, maxBufferSize)

    n, err := poller.BgpConn.Socket.Read(buf)
    if err != nil || n <= 0 {
        return fmt.Errorf("vty login socket read error: %v", err)
    }

    if strings.Contains(string(buf[:n]), "Password") {
        poller.BgpConn.Socket.Write([]byte(passwd))
        log.V(2).Infof("vty login ok")
        return nil
    }

    return fmt.Errorf("vty login failed")
}

func (poller *BgpPoller) processMessage(response []byte, neighbor string, cmd Command) {
    if (cmd == nil) {
        return
    }

    //  Get the response from vty command
    //  trim the message and get the json data
    message := strings.SplitN(string(response), "{", 2)

    var json_data string
    // json output data { }
    if (len(message) <= 1) {
        return
    }

    str := "{" + message[1]
    // trim data after last '}'
    json_data, errs := TrimSuffix(str, "}")
    if errs != nil {
        log.V(1).Infof("Error trim data : %v", errs)
        return
    }

    // Unmarshal json to BGP data
    if err := cmd.unmarshalMessage([]byte(json_data), poller, neighbor); err != nil {
        log.V(1).Infof("Error Unmarshal json to BGP data: %v", err)
    }
}

func (poller *BgpPoller) process_response(neighbor string, cmd Command) {
    buf := make([]byte, maxBufferSize)

    n, err := poller.BgpConn.Socket.Read(buf)
    if err != nil  {
        log.V(1).Infof("BGP socket read error: %v", err)
        if err != io.EOF {
            log.V(1).Infof("BGP socket read error: %v, try to reconnect", err)
            return
        }
    }

    if n > 0 {
        poller.processMessage(buf[:n], neighbor, cmd)
    }
}

func (poller *BgpPoller) run_commands() {
    for cmd, hndl := range poller.Handlers {
        hndl.run_command(poller, cmd)
    }
}

func (poller *BgpPoller) get_redisDbConn() (*redis.Client, error) {
    // check redis Db connectivity
    _, err := poller.RedisDbConn.redisClient.Ping().Result()
    if err != nil {
        log.V(2).Infof("Failed to connect to redis server %v", err)
        // reconnect to Redis DB
        redisDb, _ := poller.redisDbConn(bgpPollerCfg.TargetDB)

        // check Redis connection
        _, errors := poller.RedisDbConn.redisClient.Ping().Result()
        if errors != nil {
            return nil, fmt.Errorf("Failed to reconnect to redis server %v", errors)
        }

        poller.RedisDbConn.redisClient = redisDb
    }

    return  poller.RedisDbConn.redisClient, nil
}

// Write BGP metrics to Redis DB
func (poller *BgpPoller) Write_redisDb() {
    // Write BGP metrics to Redis STATE_DB BGP table
    redisDb, err := poller.get_redisDbConn()
    if err != nil {
        log.V(2).Infof("Write_redisDb() error: redisDB connect error")
        return
    }

    // For each neighbor
    for k, v := range poller.BgpData {
        bgp_table := dbTable + k
        val := reflect.ValueOf(&v).Elem()
        typeOfObj := val.Type()

        // write each metric to redis DB
        for i := 0; i < val.NumField(); i++ {
            fieldType := val.Field(i)

            bgp_key := typeOfObj.Field(i).Name
            value := fieldType.Interface()
            // skip unsupported metrics without BGP vty patch
            if ((bgp_key == "BgpPrefixOutPrefixes") && (value == -1)) {
                continue
            }
            // check if value changed
            val, errval := redisDb.HGet(bgp_table, bgp_key).Result()

            if ((errval != redis.Nil) && (errval != nil)) {
                log.V(1).Infof("Redis get field failed for %v, field = %v, val: %v", bgp_table, bgp_key, errval)
                return
            }

	    if (val == value) {
                continue
            }

            log.V(5).Infof("Redis  write: %v, field = %v, val: %v", bgp_table, bgp_key, value)

            err := redisDb.HSet(bgp_table, bgp_key, value).Err()
            if (err != nil) {
                log.V(2).Infof("Write to STATE_DB BGP_TABLE failed for neighbor: %s err: %v", k, err)
            }
        }
    }
}

// connect to Redis DB
func (poller *BgpPoller) redisDbConn(dbName string) (*redis.Client, error) {
        if dbName == db_others {
            return nil, fmt.Errorf("invalid Redis DB name %q", dbName)
        }

        if poller.RedisDbConn.redisClient != nil {
            poller.RedisDbConn.redisClient.Close()
        }

        dbn := spb.Target_value[dbName]
        // DB connector for direct redis operation
        redisDb := redis.NewClient(&redis.Options{
                              Network:     "tcp",
                              Addr:        bgpPollerCfg.RedisLocalTcpPort, // Default_REDIS_LOCAL_TCP_PORT,
                              Password:    "",
                              DB:          int(dbn),
                              DialTimeout: bgpPollerCfg.RedisDialTimeOut,
                           })

        if redisDb == nil {
            return nil, fmt.Errorf("Failed to create Redis Client")
        }

        // check the Redis connection
        _, err := redisDb.Ping().Result()
        if err != nil {
            return nil, fmt.Errorf("Failed to connect to redis server %v", err)
        }

        poller.RedisDbConn.redisClient = redisDb

        return redisDb, nil
}

// interface for different BGP messages/command responses
func (poller *BgpPoller) init_handlers() {
        poller.Handlers = make(map[string]Command)
        poller.SummaryCmdOnly = false

        // handlers for BGP commands
        poller.Handlers["bgp_summary"] = BGPSummaryMessage{}
        poller.Handlers["bgp_neighbor"] = BGPNeighborMessage{}
}

// periodic function to:
//    -  send, receive and process BGP vty commands
//    -  update and write BGP metrics to Redis DB
func (poller *BgpPoller) run() {
    poller.run_commands()

    // Write BGP metrics to Redis DB
    poller.Write_redisDb()
}

func (poller *BgpPoller) Close() {
    poller.BgpConn.Socket.Close()
    poller.RedisDbConn.redisClient.Close()
}

func (poller *BgpPoller) poll(ctx context.Context) error {
    ticker := time.NewTicker(bgpPollerCfg.PollingInterval)
    defer ticker.Stop()
    for {
        select {
            case <- ticker.C:
                // send and process the vty commads
                poller.run()
            case <-ctx.Done():
                log.V(2).Infof("Terminated")
                // Terminated close bgp and redis connections
                poller.Close()
                return ctx.Err()
        }
    }
}

// connect to BGP vty socket
func (poller *BgpPoller) bgpConn() error {
    // connect to BGP docker
    connection, err := net.DialTimeout("tcp", bgpPollerCfg.TCPPort, bgpPollerCfg.DialTimeOut)
    if err != nil {
        poller.BgpConn.Socket = nil
        return fmt.Errorf("Failed to connect to BGP docker, err: %v", err)
    }

    poller.BgpConn.Socket = connection
    if err := poller.login(); err != nil {
        return fmt.Errorf("Failed to login to BGP vty, err: %v", err)
    }

    return nil
}

// reconnect to BGP docker
func (poller *BgpPoller) bgpReConn() error {
    // close the current socket
    if  poller.BgpConn.Socket != nil {
        poller.BgpConn.Socket.Close()
    }

    // reconnect to BGP docker
    if err := poller.bgpConn(); err != nil {
        return fmt.Errorf("Re-Connect to BGP failed")
    }
    return nil
}

func (poller *BgpPoller) init_poll() error {
    // BGP vty command handlers
    poller.init_handlers()

    // init BGP metrics cache
    poller.BgpData = make(map[string]BgpMetrics)

    // connect to Redis DB
    _, errors := poller.redisDbConn(bgpPollerCfg.TargetDB)
    if errors != nil {
        return fmt.Errorf("Connect to Redis DB failed")
    }

    // connect to BGP docker VTY TCP port
    if err := poller.bgpConn(); err != nil {
        return fmt.Errorf("Connect to BGP failed")
    }

    return nil
}

func init() {
     flag.DurationVar(&bgpPollerCfg.PollingInterval, "poll_interval",  10*time.Second,
                      "Interval at which poller tries to reconnect to BGP docker")
}

func main() {
    // parse command line options
    flag.Parse()

    ctx, cancel := context.WithCancel(context.Background())

    log.V(2).Infof("Start BGP metrics")

    // Terminate on Ctrl+C
    go func() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	<-c
	cancel()
    }()

    // initialization
    poller := &BgpPoller{}
    if err := poller.init_poll(); err != nil {
        log.V(2).Infof("Init poll failed %v", err)
    }

    // start to poll the BGP metrics
    poller.poll(ctx)

    log.Flush()
}
