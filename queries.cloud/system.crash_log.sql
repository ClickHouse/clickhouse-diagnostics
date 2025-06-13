SELECT 
  *
FROM clusterAllReplicas(default, system.crash_log)
FORMAT Native
