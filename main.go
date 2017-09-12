package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/pborman/uuid"
)

var logger *log.Logger

func main() {
	logger = log.New(os.Stdout, "[oomdoom] ", log.LstdFlags)

	oomExe := flag.String("oom-prog", "", "oom prog")
	memLimB := flag.Uint64("mem-limit-bytes", 0, "cgroup mem and memsw limit in bytes")
	breathingRoomKB := flag.Uint64("breathing-room-kb", 0, "KB to leave remaining in cgroup")
	concurrentOoms := flag.Int("n", 0, "concurrent oom processes")
	flag.Parse()

	for i := 0; i < *concurrentOoms; i++ {
		go runOomProcesses(*oomExe, *memLimB, *breathingRoomKB, i)
	}

	for {
		time.Sleep(time.Hour)
	}
}

func runOomProcesses(oomExe string, memLimB, breathingRoomKB uint64, workerID int) {
	for {
		logger.Printf("worker %d running oom process with memory limit %d", workerID, memLimB)

		id := uuid.New()
		cgroupPath := "/sys/fs/cgroup/memory/" + id
		must("creating group path", os.Mkdir(cgroupPath, 0755))

		memLimStr := fmt.Sprintf("%d", memLimB)
		must("writing mem limit", ioutil.WriteFile(cgroupPath+"/memory.limit_in_bytes", []byte(memLimStr), 0))
		must("writing memsw limit", ioutil.WriteFile(cgroupPath+"/memory.memsw.limit_in_bytes", []byte(memLimStr), 0))
		must("setting swappiness", ioutil.WriteFile(cgroupPath+"/memory.swappiness", []byte("100"), 0))

		memLimKB := memLimB / 1024
		bulkOfMem := memLimKB * 19 / 20

		hogMemoryCmd := exec.Command(oomExe, fmt.Sprintf("%d", bulkOfMem))
		hogMemoryCmd.Stdout = os.Stdout
		hogMemoryCmd.Stderr = os.Stderr
		must("launching oom process", hogMemoryCmd.Start())

		cgroupProcs := cgroupPath + "/cgroup.procs"
		must("adding 1st process to cgroup", ioutil.WriteFile(cgroupProcs, []byte(fmt.Sprintf("%d", hogMemoryCmd.Process.Pid)), 0))
		actualMemUsageB := awaitPaging(cgroupPath)

		logger.Printf("worker %d (uuid %s) paged\n", workerID, id)
		remainingKB := (memLimB - uint64(actualMemUsageB)) / 1024
		logger.Printf("worker %d %dKB remaining, will leave %dKB\n", workerID, remainingKB, breathingRoomKB)
		remainderCmd := exec.Command(oomExe, fmt.Sprintf("%d", remainingKB-breathingRoomKB))
		remainderCmd.Stdout = os.Stdout
		remainderCmd.Stderr = os.Stderr
		must("launching remainder process", remainderCmd.Start())
		must("adding 2nd process to cgroup", ioutil.WriteFile(cgroupProcs, []byte(fmt.Sprintf("%d", remainderCmd.Process.Pid)), 0))

		// Ignore errors on purpose, this will always fail with OOM
		hogDone := make(chan struct{})
		remainderDone := make(chan struct{})
		go func() {
			hogMemoryCmd.Wait()
			logger.Printf("worker %d hog exited\n", workerID)
			close(hogDone)
		}()
		go func() {
			remainderCmd.Wait()
			logger.Printf("worker %d remainder exited\n", workerID)
			close(remainderDone)
		}()

		<-hogDone
		<-remainderDone

		must("remove cgroup path", os.Remove(cgroupPath))

		logger.Println("done running oom process")
	}
}

func awaitPaging(cgroupPath string) int {
	for {
		memUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.usage_in_bytes")
		must("reading mem usage", err)
		memUsage, err := strconv.Atoi(strings.TrimSpace(string(memUsageStr)))
		must("converting mem usage", err)

		memswUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.memsw.usage_in_bytes")
		must("reading memsw usage", err)
		memswUsage, err := strconv.Atoi(strings.TrimSpace(string(memswUsageStr)))
		must("converting memsw usage", err)

		if memswUsage != memUsage {
			return memUsage
		}

		time.Sleep(time.Second)
	}
}

func must(action string, err error) {
	if err != nil {
		logger.Printf("error %s: %s\n", action, err)
	}
}
