import json, sys
rows = []
for l in sys.stdin:
    e = json.loads(l)
    rows.append((e["sender"], e["queue_name"], e["queue_id"]))
for s, q, i in sorted(rows):
    print(f"{q:9} {i:12} {s}")
