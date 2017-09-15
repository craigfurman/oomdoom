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
	bulkOfMemKB := memLimKB * 19 / 20

	hogMemoryCmd := runInCgroup(cgroupPath, hogProg, fmt.Sprintf("%d", bulkOfMemKB))
	hogDone, err := launch(hogMemoryCmd, fmt.Sprintf("worker %d hog done", workerID))
	must("launching hog process", err)

	actualMemUsageB, actualMemswUsageB := awaitPaging(cgroupPath)

	logger.Printf("worker %d (uuid %s) paged\n", workerID, id)
	logger.Printf("worker %d %dB mem, %dB memsw remaining\n", workerID, memLimB-actualMemUsageB, memLimB-actualMemswUsageB)

	remainderMemKB := (memLimB - actualMemswUsageB + 4096) / 1024
	logger.Printf("worker %d starting remainder with %dKB\n", workerID, remainderMemKB)
	remainderCmd := runInCgroup(cgroupPath, hogProg, fmt.Sprintf("%d", remainderMemKB))
	remainderDone, err := launch(remainderCmd, fmt.Sprintf("worker %d remainder done", workerID))
	must("launching remainder process", err)

	select {
	case <-hogDone:
		logger.Printf("worker %d killing remainder process\n", workerID)
		must("killing remainder process", kill(remainderCmd))
	case <-remainderDone:
		logger.Printf("worker %d killing hog process\n", workerID)
		must("killing hog process", kill(hogMemoryCmd))
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
		must("killing hog process", kill(hogMemoryCmd))
		must("killing remainder process", kill(remainderCmd))
	}

	must("remove cgroup path", os.Remove(cgroupPath))

	logger.Printf("worker %d done running experiment\n", workerID)
}

func launch(cmd *exec.Cmd, doneMsg string) (<-chan struct{}, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan struct{})
	go func(d chan<- struct{}) {
		// Ignore errors on purpose, this will always fail with OOM
		cmd.Wait()
		logger.Println(doneMsg)
		close(d)
	}(done)

	return done, nil
}

func kill(cmd *exec.Cmd) error {
	if err := cmd.Process.Kill(); err != nil {
		if err.Error() == "os: process already finished" {
			return nil
		}
		return err
	}

	cmd.Process.Wait()
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

func awaitPaging(cgroupPath string) (uint64, uint64) {
	for {
		memUsage, memswUsage := readActualMemoryUsage(cgroupPath)

		if memswUsage != memUsage {
			return memUsage, memswUsage
		}

		time.Sleep(time.Second)
	}
}

func setupLimits(cgroupPath string, memLimB uint64) {
	memLimStr := fmt.Sprintf("%d", memLimB)
	must("writing mem limit", ioutil.WriteFile(cgroupPath+"/memory.limit_in_bytes", []byte(memLimStr), 0))
	must("writing memsw limit", ioutil.WriteFile(cgroupPath+"/memory.memsw.limit_in_bytes", []byte(memLimStr), 0))
	must("setting swappiness", ioutil.WriteFile(cgroupPath+"/memory.swappiness", []byte("100"), 0))
}

func must(action string, err error) {
	if err != nil {
		logger.Printf("error %s: %s\n", action, err)
		os.Exit(1)
	}
}
