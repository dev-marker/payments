-- How long the transfers of the last two minutes took, from the 202 to their
-- final state, in milliseconds: the saga, the part a load test of the API
-- never sees.
SELECT state, count(*),
       round(percentile_cont(0.5) WITHIN GROUP (ORDER BY ms)) AS p50,
       round(percentile_cont(0.95) WITHIN GROUP (ORDER BY ms)) AS p95,
       round(percentile_cont(0.99) WITHIN GROUP (ORDER BY ms)) AS p99
FROM (SELECT state, 1000 * extract(epoch FROM updated_at - created_at) AS ms
      FROM transfers WHERE created_at > now() - interval '2 minutes') AS t
GROUP BY state;
