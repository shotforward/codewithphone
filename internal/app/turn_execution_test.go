package app

import "testing"

func TestPlanTurnExecutionProjectIntroNotForcedReadOnly(t *testing.T) {
	profile := planTurnExecution("帮我介绍下这个项目")
	if profile.ReadOnly {
		t.Fatal("expected project introduction prompt to avoid forced read-only")
	}
	if !profile.TrackChanges {
		t.Fatal("expected project introduction prompt to keep change tracking enabled")
	}
}

func TestPlanTurnExecutionWritePrompt(t *testing.T) {
	profile := planTurnExecution("请修改 README.md 并新增一段安装说明")
	if profile.ReadOnly {
		t.Fatal("expected edit prompt to allow writes")
	}
	if !profile.TrackChanges {
		t.Fatal("expected edit prompt to track changes")
	}
}

func TestPlanTurnExecutionExplicitReadOnlyEnglish(t *testing.T) {
	profile := planTurnExecution("Explain this repository in read-only mode and do not modify files.")
	if !profile.ReadOnly {
		t.Fatal("expected explicit read-only prompt to be read-only")
	}
}

func TestPlanTurnExecutionGreetingPrompt(t *testing.T) {
	profile := planTurnExecution("你好")
	if !profile.ReadOnly {
		t.Fatal("expected greeting prompt to be read-only")
	}
	if profile.TrackChanges {
		t.Fatal("expected greeting prompt to skip change tracking")
	}
}

func TestPlanTurnExecutionGreetingWithTask(t *testing.T) {
	profile := planTurnExecution("你好，帮我修改 README")
	if profile.ReadOnly {
		t.Fatal("expected task prompt to allow writes")
	}
}
