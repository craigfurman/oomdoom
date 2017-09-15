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
		go runExperiments(*hogProg, *memLimB, i)
	}

	for {
		time.Sleep(time.Hour)
	}
}

func runExperiments(hogProg string, memLimB uint64, workerID int) {
	for {
		runExperiment(hogProg, memLimB, workerID)
	}
}

func runExperiment(hogProg string, memLimB uint64, workerID int) {
	logger.Printf("worker %d running experiment with memory limit %d", workerID, memLimB)

	id := uuid.New()
	cgroupPath := "/sys/fs/cgroup/memory/" + id
	must("creating group path", os.Mkdir(cgroupPath, 0755))
	setupLimits(cgroupPath, memLimB)

	memLimKB := memLimB / 1024
	fourtyFivePercentStr := fmt.Sprintf("%d", memLimKB*9/20)

	hogMemoryCmd := runInCgroup(cgroupPath, hogProg, fourtyFivePercentStr, fourtyFivePercentStr)
	hogDone, err := launch(hogMemoryCmd, fmt.Sprintf("worker %d hog done", workerID))
	must("launching hog process", err)

	_, actualMemswUsageB := readActualMemoryUsage(cgroupPath)

	halfOfRemainingHeadroom := fmt.Sprintf("%d", ((memLimB-actualMemswUsageB)/1024-128)/2)
	approachEdgeCmd := runInCgroup(cgroupPath, hogProg, halfOfRemainingHeadroom, halfOfRemainingHeadroom)
	approachingEdgeDone, err := launch(approachEdgeCmd, fmt.Sprintf("worker %d approaching-edge done", workerID))
	must("launching approaching-edge process", err)

	finalRegularPagesKB, finalHugePagesKB := calculcateMemoryForFinalProcess(workerID, cgroupPath, memLimB)
	finalCmd := runInCgroup(cgroupPath, hogProg, fmt.Sprintf("%d", finalRegularPagesKB), fmt.Sprintf("%d", finalHugePagesKB))
	finalDone, err := launch(finalCmd, fmt.Sprintf("worker %d final done", workerID))
	must("launching final process", err)

	time.Sleep(time.Millisecond * 500)

	accessProcessMemory(hogMemoryCmd)
	accessProcessMemory(approachEdgeCmd)
	accessProcessMemory(finalCmd)

	logger.Printf("worker %d killing processes\n", workerID)
	must("killing processes", killAll(hogMemoryCmd, approachEdgeCmd, finalCmd))
	<-hogDone
	<-approachingEdgeDone
	<-finalDone

	must("remove cgroup path", os.Remove(cgroupPath))
	logger.Printf("worker %d done running experiment\n", workerID)
}

func launch(cmd *exec.Cmd, doneMsg string) (<-chan struct{}, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Wait for mallocs and mmaps to complete
	time.Sleep(time.Millisecond * 500)

	done := make(chan struct{})
	go func(d chan<- struct{}) {
		// Ignore errors on purpose, this will always fail with OOM
		cmd.Wait()
		logger.Println(doneMsg)
		close(d)
	}(done)

	return done, nil
}

func calculcateMemoryForFinalProcess(workerID int, cgroupPath string, memLimB uint64) (uint64, uint64) {
	actualMemUsageB, actualMemswUsageB := readActualMemoryUsage(cgroupPath)
	logger.Printf("worker %d %dB mem, %dB memsw remaining\n", workerID, memLimB-actualMemUsageB, memLimB-actualMemswUsageB)

	if workerID%3 == 0 {
		// huge page
		return 0, (memLimB / 5) / 1024
	}

	if workerID%3 == 1 {
		return (memLimB-actualMemswUsageB)/1024 + 32, 0
	}

	if workerID%3 == 2 {
		return ((memLimB - actualMemswUsageB) / 2) / 1024, 0
	}

	panic("unreachable")
}

func killAll(cmds ...*exec.Cmd) error {
	for _, cmd := range cmds {
		if err := kill(cmd); err != nil {
			return err
		}
	}

	return nil
}

func kill(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		if err.Error() == "os: process already finished" {
			return nil
		}
		return err
	}

	return nil
}

func readActualMemoryUsage(cgroupPath string) (uint64, uint64) {
	memUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.usage_in_bytes")
	must("reading mem usage", err)
	memUsage, err := strconv.ParseUint(strings.TrimSpace(string(memUsageStr)), 10, 64)
	must("converting mem usage", err)

	memswUsageStr, err := ioutil.ReadFile(cgroupPath + "/memory.memsw.usage_in_bytes")
	must("reading memsw usage", err)
	memswUsage, err := strconv.ParseUint(strings.TrimSpace(string(memswUsageStr)), 10, 64)
	must("converting memsw usage", err)

	return memUsage, memswUsage
}

func runInCgroup(cgroupPath string, argv ...string) *exec.Cmd {
	shellCmd := fmt.Sprintf("echo $$ > %s/cgroup.procs && exec %s", cgroupPath, strings.Join(argv, " "))
	cmd := exec.Command("bash", "-c", shellCmd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

func setupLimits(cgroupPath string, memLimB uint64) {
	memLimStr := fmt.Sprintf("%d", memLimB)
	must("writing mem limit", ioutil.WriteFile(cgroupPath+"/memory.limit_in_bytes", []byte(memLimStr), 0))
	must("writing memsw limit", ioutil.WriteFile(cgroupPath+"/memory.memsw.limit_in_bytes", []byte(memLimStr), 0))
	must("setting swappiness", ioutil.WriteFile(cgroupPath+"/memory.swappiness", []byte("100"), 0))
}

func accessProcessMemory(cmd *exec.Cmd) {
	ioutil.ReadFile(fmt.Sprintf("/proc/%d/environ", cmd.Process.Pid))
	ioutil.ReadFile(fmt.Sprintf("/proc/%d/cmdline", cmd.Process.Pid))
}

func must(action string, err error) {
	if err != nil {
		logger.Printf("error %s: %s\n", action, err)
		os.Exit(1)
	}
}
