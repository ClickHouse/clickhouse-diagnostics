package main

import "strings"

// sharedMergeTreeHint returns a warning when -mode onprem is used against a
// server that runs the SharedMergeTree stack (cloud_mode = 1 in
// system.settings). On such a cluster every replica keeps its own system
// tables (parts, errors, part_log, query_log, text_log …), so the onprem
// collectors — plain system.* references — describe one node of N. That is
// the right choice for host facts and log files, but a reader of the bundle
// must know the narrowing happened; the hint also names the alternative.
//
// cloudModeValue is the raw result of
//
//	SELECT value FROM system.settings WHERE name = 'cloud_mode'
//
// An empty value (setting absent, query failed, no grant) yields no hint.
func sharedMergeTreeHint(mode, cloudModeValue string) string {
	if strings.ToLower(strings.TrimSpace(mode)) != "onprem" {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(cloudModeValue))
	if v != "1" && v != "true" {
		return ""
	}
	return "Warning: this server runs with cloud_mode = 1 (SharedMergeTree / shared-storage cluster). " +
		"-mode onprem collects the system tables of THIS node only — parts, errors, part_log, " +
		"query_log and text_log of the other replicas are not in the bundle. " +
		"To cover every replica run again with -mode cloud (clusterAllReplicas over the 'default' cluster; " +
		"needs GRANT REMOTE and CREATE TEMPORARY TABLE), keeping this onprem bundle for host facts, " +
		"configuration and log files."
}
