package main

import "strings"

// sharedMergeTreeHint 在 -mode onprem 用于运行 SharedMergeTree 栈
// （system.settings 中 cloud_mode = 1）的服务器时返回一条警告。在这样的集群上，
// 每个 replica 都保有自己的系统表（parts、errors、part_log、query_log、text_log …），
// 因此 onprem collector——直接引用 system.*——只描述 N 个节点中的一个。对于
// host facts 和日志文件这是正确的选择，但 bundle 的读者必须知道发生了范围收窄；
// 该提示也会说明可选的替代方案。
//
// cloudModeValue 是以下查询的原始结果
//
//	SELECT value FROM system.settings WHERE name = 'cloud_mode'
//
// 空值（设置不存在、查询失败、无授权）不会产生提示。
func sharedMergeTreeHint(mode, cloudModeValue string) string {
	if strings.ToLower(strings.TrimSpace(mode)) != "onprem" {
		return ""
	}
	v := strings.ToLower(strings.TrimSpace(cloudModeValue))
	if v != "1" && v != "true" {
		return ""
	}
	return "Warning: 此服务器以 cloud_mode = 1 运行（SharedMergeTree / 共享存储集群）。" +
		"-mode onprem 只采集本节点的系统表——其他 replica 的 parts、errors、part_log、" +
		"query_log 和 text_log 不在 bundle 中。" +
		"要覆盖每个 replica，请使用 -mode cloud 重新运行（通过 'default' 集群执行 clusterAllReplicas；" +
		"需要 GRANT REMOTE 和 CREATE TEMPORARY TABLE），同时保留此 onprem bundle 以获取 host facts、" +
		"配置和日志文件。"
}
