package client

import (
	"encoding/json"
	"runtime"
	log "github.com/golang/glog"
)

func getRuntimeMemStat() ([]byte, error) {
	type memStats struct {
		Alloc       uint64      `json:"alloc"`
		TotalAlloc  uint64      `json:"totalAlloc"`
		SysMem      uint64      `json:"sysMem"`
		FreeMem     uint64      `json:"frees"`
	}

        var m runtime.MemStats
        runtime.ReadMemStats(&m)

	resp := &memStats{
		Alloc:       m.Alloc,
		TotalAlloc:  m.TotalAlloc,
		SysMem:	     m.Sys,
		FreeMem:     m.Frees,
	}

        b, err := json.Marshal(resp)
        if err != nil {
                log.V(2).Infof("%v", err)
                return b, err
        }
        log.V(4).Infof("getProcStat, output %v", string(b))
        return b, nil
}

func getRuntimeProcessInfo() ([]byte, error) {
	type procInfo struct {
		NumThreads   int      `json:"numThreads"`
		CPUPercent   int      `json:"cpuPercent"`
	}

	// get the runtime CPU info
	resp := &procInfo{
		NumThreads:   1,
		CPUPercent:   0,
	}

        b, err := json.Marshal(resp)
        if err != nil {
                log.V(2).Infof("%v", err)
                return b, err
        }
        log.V(4).Infof("getProcStat, output %v", string(b))
        return b, nil
}

func getKeepAlive() ([]byte, error) {
	type keepAlives struct {
		Keepalive   int      `json:"keepalive"`
	}

	resp := &keepAlives{
		Keepalive: 1,
	}

        b, err := json.Marshal(resp)
        if err != nil {
                log.V(2).Infof("%v", err)
                return b, err
        }
        log.V(4).Infof("getProcKeepAlive, output %v", string(b))
        return b, nil
}
