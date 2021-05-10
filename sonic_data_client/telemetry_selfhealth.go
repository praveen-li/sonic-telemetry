package client

import (
	"encoding/json"
	"runtime"
	log "github.com/golang/glog"
	cpu "github.com/shirou/gopsutil/cpu"
	net "github.com/shirou/gopsutil/net"
	process "github.com/shirou/gopsutil/process"
)


/*
 gopsutil struct for process info per process
*/
type Process struct {
        Background    bool                    `json:"background"`
        CPUPercent    float64                 `json:"cpupercent"`
        CreateTime    int64                   `json:"createtime"`
        Connections   []net.ConnectionStat    `json:"connections"`
        Foreground    bool                    `json:"foreground"`
        IsRunning     bool                    `json:"isrunning"`
        MemoryInfo    *process.MemoryInfoStat `json:"memoryinfo"`
        MemoryPercent float32                 `json:"memorypercent"`
        Nice          int32                   `json:"nice"`
        NumFDs        int32                   `json:"numfds"`
        NumThreads    int32                   `json:"numthreads"`
        OpenFiles     []process.OpenFilesStat `json:"openFiles"`
        Pid           int32                   `json:"pid"`
        Ppid          int32                   `json:"ppid"`
        Status        string                  `json:"status"`
        Times         *cpu.TimesStat          `json:"times"`
}


func getRuntimeGoInfo() ([]byte, error) {
	type goInfo struct {
		NumGoroutine int  `json:"numgoRoutine"`
		NumCgoCall   int64    `json:"numcgoCall"`
	}

	// get the runtime goroutines number and cgo call info
	resp := &goInfo{
		NumGoroutine: runtime.NumGoroutine(),
		NumCgoCall:   runtime.NumCgoCall(),
	}

	b, err := json.MarshalIndent(resp, "", "    ")
	if err != nil {
		log.V(2).Infof("%v", err)
		return b, err
	}

	log.V(4).Infof("getGoInfo, output %v", string(b))
	return b, nil
}


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


// Returns Process Information per running process.
func getRuntimeProcessInfo() ([]byte, error) {

	ProcessList, _ := process.Processes()
	p := make([]Process, len(ProcessList))
	processinfo := make(map[string]Process)

	if len(ProcessList) > 0 {

		for i := 0; i < len(ProcessList); i++ {
			p[i].Background, _ = ProcessList[i].Background()
			p[i].CPUPercent, _ = ProcessList[i].CPUPercent()
			p[i].CreateTime, _ = ProcessList[i].CreateTime()
			p[i].Connections, _ = ProcessList[i].Connections()
			p[i].Foreground, _ = ProcessList[i].Foreground()
			p[i].IsRunning, _ = ProcessList[i].IsRunning()
			p[i].MemoryInfo, _ = ProcessList[i].MemoryInfo()
			p[i].MemoryPercent, _ = ProcessList[i].MemoryPercent()
			p[i].Nice, _ = ProcessList[i].Nice()
			p[i].NumFDs, _ = ProcessList[i].NumFDs()
			p[i].NumThreads, _ = ProcessList[i].NumThreads()
			p[i].Pid = ProcessList[i].Pid
			p[i].Ppid, _ = ProcessList[i].Ppid()
			p[i].Times, _ = ProcessList[i].Times()

			pStatus, _ := ProcessList[i].Status()

			switch {
			case pStatus == "S":
				pStatus = "sleeping"
			case pStatus == "R":
				pStatus = "running"
			case pStatus == "T":
				pStatus = "stopped"
			case pStatus == "I":
				pStatus = "idle"
			case pStatus == "Z":
				pStatus = "Zombie"
			case pStatus == "W":
				pStatus = "waiting"
			case pStatus == "L":
				pStatus = "locked"
			}

			p[i].Status = pStatus

			proc_name, _ := ProcessList[i].Name()
			processinfo[proc_name] = p[i]

		}
	}

	b, err := json.MarshalIndent(processinfo, "", "    ")
	if err != nil {
		log.V(2).Infof("%v", err)
		return b, err
	}

	log.V(4).Infof("Process Information per Process, output %v", string(b))
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
