package collection

import (
	"strings"
	"testing"
)

func TestSearchQueryIntersectsStarFloorAndShard(t *testing.T) {
	for _, tc := range []struct {
		name, query, want string
		floor, min, max   int
		sharded           bool
	}{
		{name: "默认门槛", query: "topic:mcp", floor: 200, want: "topic:mcp stars:>=200"},
		{name: "更严格的查询", query: "topic:mcp stars:>500", floor: 200, want: "topic:mcp stars:>=501"},
		{name: "并集分支收紧", query: "topic:mcp stars:20..199 stars:150..350 stars:500..*", floor: 200, want: "topic:mcp stars:200..350 stars:>=500"},
		{name: "旧分片取交集", query: "topic:mcp stars:<=400", floor: 200, sharded: true, min: 150, max: 250, want: "topic:mcp stars:200..250"},
		{name: "零门槛", query: "topic:mcp", want: "topic:mcp"},
		{name: "保留短语和排除词", query: `"mentions stars:2" in:readme -stars:300`, floor: 200, want: `"mentions stars:2" in:readme -stars:300 stars:>=200`},
		{name: "原查询上限低于门槛", query: "topic:mcp stars:<200", floor: 200},
		{name: "旧分片上限低于门槛", query: "topic:mcp", floor: 200, sharded: true, max: 199},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := Payload{Query: tc.query, StarFloor: tc.floor, StarRange: tc.sharded, MinStars: tc.min, MaxStars: tc.max}
			query, err := p.SearchQuery()
			if err != nil {
				t.Fatal(err)
			}
			got, _, _ := strings.Cut(query, " is:public")
			if got != tc.want || (tc.want == "" && query != "") {
				t.Fatalf("得到 %q，期望 %q", query, tc.want)
			}
		})
	}
	children, err := SplitSearch(Payload{StarFloor: 200}, new(1000))
	if err != nil || len(children) != 2 || children[0].MinStars != 200 || children[1].StarFloor != 200 ||
		children[0].MaxStars+1 != children[1].MinStars {
		t.Fatalf("分片未继承配置门槛: %+v %v", children, err)
	}
}
