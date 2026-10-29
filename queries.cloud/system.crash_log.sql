-- tier: essential
SELECT 
  *
FROM clusterAllReplicas(default, system.crash_log)
