package pkg

import (
	"reflect"
	"testing"
)

func TestExtractTables(t *testing.T) {
	cases := []struct {
		name string
		sql  string
		want []string
	}{
		{
			name: "single direct reference",
			sql:  "SELECT * FROM system.parts WHERE active = 1",
			want: []string{"system.parts"},
		},
		{
			name: "cluster-all-replicas wrapper still reports the inner table",
			sql:  "SELECT * FROM clusterAllReplicas(default, system.query_log) WHERE event_time > now() - INTERVAL 7 DAY",
			want: []string{"system.query_log"},
		},
		{
			name: "join across two system tables",
			sql:  "SELECT * FROM system.tables AS t LEFT JOIN system.parts AS p ON t.database = p.database",
			want: []string{"system.parts", "system.tables"},
		},
		{
			name: "deduplicates repeated references",
			sql:  "SELECT * FROM system.query_log WHERE query_id IN (SELECT query_id FROM system.query_log WHERE x = 1)",
			want: []string{"system.query_log"},
		},
		{
			name: "merge() table function reported as pattern",
			sql:  "SELECT * FROM clusterAllReplicas(default, merge(system, '^asynchronous_insert_log'))",
			want: []string{"system.* matching '^asynchronous_insert_log'"},
		},
		{
			name: "no tables in pure-function SELECTs",
			sql:  "SELECT version(), now(), uptime()",
			want: []string{},
		},
		{
			name: "case-insensitive matching",
			sql:  "SELECT * FROM SYSTEM.PARTS",
			want: []string{"system.parts"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractTables(tc.sql)
			if len(got) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ExtractTables(%q) = %v, want %v", tc.sql, got, tc.want)
			}
		})
	}
}
