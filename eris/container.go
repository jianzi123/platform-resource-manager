package main

// #include <pqos.h>
import "C"

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"syscall"
)

type Container struct {
	file                *os.File
	cpuFile             *os.File
	name                string
	id                  string
	fds                 [][]uintptr
	perfLastValue       [][]uint64
	perfLastEnabled     []uint64
	perfLastRunning     []uint64
	lastCPUUsage        []uint64 // {cpu usage, system usage}
	lastCPUUsage1       []uint64 // {cpu usage, system usage}
	cacheOccupancySum   uint64
	cacheOccupancyCount uint64
	pqosLastValue       []uint64
	pqosMonitorData     *C.struct_pqos_mon_data
	pqosPidsMap         map[C.pid_t]bool
	monitorStarted      bool
	isLatencyCritical   bool
	isBestEffort        bool
}

func newContainer(id, name string) (*Container, error) {
	path, cpuPath := getCgroupPath(id), getCgroupCPUPath(id)
	ret := Container{name: name, id: id, monitorStarted: false}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	ret.file = f

	cpuf, err := os.Open(cpuPath)
	if err != nil {
		return nil, err
	}
	ret.cpuFile = cpuf

	pidsMap, err := listTaskPid(id)
	if err != nil {
		log.Println(err)
	} else {
		ret.pqosMonitorData, err = newPqosGroup(id, pidsMap)
		ret.pqosPidsMap = pidsMap
		if err != nil {
			log.Println(err)
		}
	}
	ret.fds = make([][]uintptr, numCPU)
	for i := 0; i < numCPU; i++ {
		ret.fds[i] = make([]uintptr, len(peCounters))
		ret.fds[i][0], err = openPerfLeader(ret.file.Fd(), uintptr(i), peCounters[0])
		if err != nil {
			log.Println(err)
			continue
		}
		for j := 1; j < len(peCounters); j++ {
			ret.fds[i][j], err = openPerfFollower(ret.fds[i][0], ret.file.Fd(), uintptr(i), peCounters[j])
			if err != nil {
				log.Println(err)
			}
		}
	}
	if _, ok := latencyCritical[name]; ok {
		ret.isLatencyCritical = true
	}
	if _, ok := bestEffort[name]; ok {
		ret.isBestEffort = true
	}
	return &ret, nil
}

func (c *Container) start() {
	for i := 0; i < numCPU; i++ {
		for j := 0; j < len(peCounters); j++ {
			err := startPerf(c.fds[i][j])
			if err != nil {
				log.Print(err)
			}
		}
	}
}

func (c *Container) pollCacheOccupancy() (uint64, error) {
	if pollPqos(c.pqosMonitorData) != nil {
		return 0, fmt.Errorf("fail to poll cache occupancy")
	}
	return uint64(c.pqosMonitorData.values.llc / 1024), nil
}

func (c *Container) pollMemoryBandwidth() []uint64 {
	if pollPqos(c.pqosMonitorData) != nil {
		return nil
	}
	v := c.pqosMonitorData.values
	var mbmValue []uint64
	mbmValue = []uint64{uint64(v.mbm_total), uint64(v.mbm_local)}

	if c.pqosLastValue == nil {
		c.pqosLastValue = mbmValue
		return nil
	}
	// last level cache is an instant value and no need to calculate delta
	ret := []uint64{}
	for i := 0; i < len(c.pqosLastValue); i++ {
		ret = append(ret, mbmValue[i]-c.pqosLastValue[i])
	}
	if ret[0] > ret[1] {
		ret = append(ret, ret[0]-ret[1])
	} else {
		ret = append(ret, 0)
	}
	c.pqosLastValue = mbmValue
	return ret
}

func (c *Container) pollPerf() []uint64 {
	newData := make([][]uint64, numCPU)
	enabled := make([]uint64, numCPU)
	running := make([]uint64, numCPU)
	for i := 0; i < numCPU; i++ {
		var err error
		newData[i], enabled[i], running[i], err = readPerf(c.fds[i][0])
		if err != nil {
			log.Print(err)
			continue
		}
	}
	var res []uint64
	if c.perfLastValue != nil {
		res = make([]uint64, len(peCounters))
		for i := 0; i < numCPU; i++ {
			for j := 0; j < len(peCounters); j++ {
				if enabled[i]-c.perfLastEnabled[i] != 0 {
					res[j] += uint64(float64(newData[i][j]-c.perfLastValue[i][j]) / float64(enabled[i]-c.perfLastEnabled[i]) * float64(running[i]-c.perfLastRunning[i]))
				}
			}
		}

	}
	c.perfLastValue = newData
	c.perfLastEnabled = enabled
	c.perfLastRunning = running

	return res
}

func (c *Container) pollCPUUsage(isMetrics bool) []uint64 {
	cpuf, err := os.Open("/proc/stat")
	if err != nil {
		panic(err)
	}
	defer cpuf.Close()
	s, err := bufio.NewReader(cpuf).ReadString('\n')
	if err != nil {
		panic(err)
	}
	data := strings.Split(s, " ")
	var sys, usage uint64
	for i := 1; i < len(data); i++ {
		v, err := strconv.ParseUint(data[i], 10, 64)
		if err != nil {
			//log.Print(err)
		} else {
			sys += v
		}
	}
	sys = sys * 10000000

	c.cpuFile.Seek(0, 0)
	fmt.Fscanf(c.cpuFile, "%d", &usage)
	lastCPUUsage := &c.lastCPUUsage1
	if isMetrics {
		lastCPUUsage = &c.lastCPUUsage
	}

	if *lastCPUUsage == nil {
		*lastCPUUsage = []uint64{usage, sys}
		return nil
	}
	ret := []uint64{usage - (*lastCPUUsage)[0], sys - (*lastCPUUsage)[1]}
	(*lastCPUUsage)[0] = usage
	(*lastCPUUsage)[1] = sys
	return ret
}

func (c *Container) finalize() {
	for i := 0; i < numCPU; i++ {
		for j := 0; j < len(peCounters); j++ {
			syscall.Close(int(c.fds[i][j]))
		}
	}
	removePqosGroup(c.id)
	c.file.Close()
	c.cpuFile.Close()
}