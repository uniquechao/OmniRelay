package project

import (
	"errors"
	"regexp"
)

var (
	errDenied      = errors.New("project access denied")
	errUnavailable = errors.New("project source unavailable")
	errConflict    = errors.New("project state changed")
	errInput       = errors.New("invalid project request")
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.:-]{0,199}$`)

type projectTypeOption struct {
	Name string `json:"name"`
	Tone string `json:"tone"`
}

var projectTypeOptions = []projectTypeOption{
	{Name: "研发", Tone: "blue"},
	{Name: "销售", Tone: "purple"},
	{Name: "售后", Tone: "amber"},
	{Name: "生产", Tone: "green"},
	{Name: "人力资源", Tone: "rose"},
	{Name: "市场活动", Tone: "teal"},
	{Name: "公共归集", Tone: "neutral"},
	{Name: "交付", Tone: "indigo"},
}

type Project struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Type         string `json:"project_type"`
	Timezone     string `json:"business_timezone"`
	Background   string `json:"background,omitempty"`
	Version      int64  `json:"version"`
	PlannedStart string `json:"planned_start,omitempty"`
	PlannedEnd   string `json:"planned_end,omitempty"`
	BoardVersion int64  `json:"board_version"`
}
