package checkrunner

import (
	"os/exec"
	"slices"
	"testing"
)

func TestConfigureTerminationPassesLocalAppDataForTheGoBuildCache(t *testing.T) {
	t.Setenv("LocalAppData", `D:\gate\localappdata`)
	command := exec.Command("go", "test")
	command.Env = []string{"GOENV=off", "PATH=x"}
	configureTermination(command, func(*exec.Cmd) error { return nil })
	if !slices.Contains(command.Env, `LocalAppData=D:\gate\localappdata`) {
		t.Fatalf("child environment omits LocalAppData: %v", command.Env)
	}
	if !slices.Contains(command.Env, "GOENV=off") || !slices.Contains(command.Env, "PATH=x") {
		t.Fatalf("existing entries changed: %v", command.Env)
	}

	again := exec.Command("go", "test")
	again.Env = command.Env
	configureTermination(again, func(*exec.Cmd) error { return nil })
	count := 0
	for _, entry := range again.Env {
		if len(entry) >= 13 && entry[:13] == "LocalAppData=" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("LocalAppData appears %d times: %v", count, again.Env)
	}
}

func TestConfigureTerminationOmitsLocalAppDataWhenTheHostHasNone(t *testing.T) {
	t.Setenv("LocalAppData", "")
	command := exec.Command("go", "test")
	command.Env = []string{"GOENV=off"}
	configureTermination(command, func(*exec.Cmd) error { return nil })
	if len(command.Env) != 1 {
		t.Fatalf("environment grew without a host value: %v", command.Env)
	}
}
