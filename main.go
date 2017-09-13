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

	hogProg := flag.String("hog-prog", "", "hog prog")
	memLimB := flag.Uint64("mem-limit-bytes", 0, "cgroup mem and memsw limit in bytes")
	concurrentJobs := flag.Int("n", 0, "concurrent processes")
	flag.Parse()

	for i := 0; i < *concurrentJobs; i++ {
		go runHogProcesses(*hogProg, *memLimB, i)
	}

	for {
		time.Sleep(time.Hour)
	}
}

func runHogProcesses(hogProg string, memLimB uint64, workerID int) {
	for {
		logger.Printf("worker %d running experiment with memory limit %d", workerID, memLimB)

		id := uuid.New()
		cgroupPath := "/sys/fs/cgroup/memory/" + id
		must("creating group path", os.Mkdir(cgroupPath, 0755))

		memLimStr := fmt.Sprintf("%d", memLimB)
		must("writing mem limit", ioutil.WriteFile(cgroupPath+"/memory.limit_in_bytes", []byte(memLimStr), 0))
		must("writing memsw limit", ioutil.WriteFile(cgroupPath+"/memory.memsw.limit_in_bytes", []byte(memLimStr), 0))
		must("setting swappiness", ioutil.WriteFile(cgroupPath+"/memory.swappiness", []byte("100"), 0))

		memLimKB := memLimB / 1024
		bulkOfMemKB := memLimKB * 19 / 20

		hogMemoryCmd := exec.Command(hogProg, fmt.Sprintf("%d", bulkOfMemKB))
		hogMemoryCmd.Stdout = os.Stdout
		hogMemoryCmd.Stderr = os.Stderr
		must("launching hog process", hogMemoryCmd.Start())

		cgroupProcs := cgroupPath + "/cgroup.procs"
		must("adding 1st process to cgroup", ioutil.WriteFile(cgroupProcs, []byte(fmt.Sprintf("%d", hogMemoryCmd.Process.Pid)), 0))
		actualMemUsageB, actualMemswUsageB := awaitPaging(cgroupPath)

		logger.Printf("worker %d (uuid %s) paged\n", workerID, id)
		logger.Printf("worker %d %dB mem, %dB memsw remaining\n", workerID, memLimB-uint64(actualMemUsageB), memLimB-uint64(actualMemswUsageB))

		remainderMemKB := (memLimB - uint64(actualMemswUsageB) + 4096) / 1024
		logger.Printf("worker %d starting remainder with %dKB\n", workerID, remainderMemKB)
		remainderCmd := exec.Command(hogProg, fmt.Sprintf("%d", remainderMemKB))
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

		select {
		case <-hogDone:
			logger.Printf("worker %d killing remainder process\n", workerID)
			must("killing remainder process", killAndWait(remainderCmd))
		case <-remainderDone:
			logger.Printf("worker %d killing hog process\n", workerID)
			must("killing hog process", killAndWait(hogMemoryCmd))
		case <-time.After(time.Second * 5):
			logger.Printf("worker %d (uuid %s) has not been OOM killed\n", workerID, id)
			actualMemB, actualMemswB := readActualMemoryUsage(cgroupPath)
			logger.Printf("worker %d mem: %dB, memsw: %dB\n", workerID, actualMemB, actualMemswB)
			if uint64(actualMemswUsageB) > memLimB {
				logger.Printf("worker %d (uuid %s) yielded a result!\n", workerID, id)
				for {
					time.Sleep(time.Hour)
				}
			}
			must("killing hog process", killAndWait(hogMemoryCmd))
			must("killing remainder process", killAndWait(remainderCmd))
		}

		must("remove cgroup path", os.Remove(cgroupPath))

		logger.Printf("worker %d done running experiment\n", workerID)
	}
}

func killAndWait(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		if err.Error() == "os: process already finished" {
			return nil
		}
		return err
	}
	cmd.Process.Wait()
	return nil
}

func readActualMemoryUsage(cgroupPath string) (int, int) {
	memUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.usage_in_bytes")
	must("reading mem usage", err)
	memUsage, err := strconv.Atoi(strings.TrimSpace(string(memUsageStr)))
	must("converting mem usage", err)

	memswUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.memsw.usage_in_bytes")
	must("reading memsw usage", err)
	memswUsage, err := strconv.Atoi(strings.TrimSpace(string(memswUsageStr)))
	must("converting memsw usage", err)

	return memUsage, memswUsage
}

func awaitPaging(cgroupPath string) (int, int) {
	for {
		memUsage, memswUsage := readActualMemoryUsage(cgroupPath)

		if memswUsage != memUsage {
			return memUsage, memswUsage
		}

		time.Sleep(time.Second)
	}
}

func must(action string, err error) {
	if err != nil {
		logger.Printf("error %s: %s\n", action, err)
		os.Exit(1)
	}
}
