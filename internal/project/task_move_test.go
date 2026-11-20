package project

import (
	"errors"
	"net/url"
	"reflect"
	"testing"
)

func TestTaskMilestoneFilters(t *testing.T) {
	for _, query := range []string{"q=合同&owner_member_id=u", "q=合同&owner_member_id=u&milestone_id=m", "q=合同&owner_member_id=u&milestone_unassigned=true"} {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		got, err := viewQuery("tasks", values)
		if err != nil || !reflect.DeepEqual(got, values) {
			t.Fatalf("组合筛选发生变化 %v %v", got, err)
		}
	}
	for _, query := range []string{"milestone_id=m&milestone_unassigned=true", "milestone_id=m&milestone_id=n", "milestone_unassigned=无"} {
		values, _ := url.ParseQuery(query)
		if _, err := viewQuery("tasks", values); !errors.Is(err, errInput) {
			t.Fatalf("应拒绝筛选 %s", query)
		}
	}
}
