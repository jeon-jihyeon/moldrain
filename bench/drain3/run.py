"""Trains Drain3 at its defaults on every line of one content file in order.

uv run --with drain3==0.9.11 python bench/drain3/run.py <content.txt> <out dir>

Writes ids.txt with the cluster id of each line, templates.json with each cluster's
final template, and stats.json with the training time and the peak resident memory.
"""

import json
import platform
import resource
import sys
import time
from importlib.metadata import version

from drain3 import TemplateMiner
from drain3.template_miner_config import TemplateMinerConfig


def main(src: str, out: str) -> None:
    # A fresh config keeps Drain3's defaults
    # Nothing reads drain3.ini
    miner = TemplateMiner(config=TemplateMinerConfig())
    with open(src, encoding="utf-8", errors="replace") as f:
        lines = [line.rstrip("\n") for line in f]
    ids = []
    start = time.perf_counter()
    for line in lines:
        ids.append(miner.add_log_message(line)["cluster_id"])
    seconds = time.perf_counter() - start
    with open(f"{out}/ids.txt", "w") as f:
        f.write("\n".join(map(str, ids)))
        f.write("\n")
    templates = {str(c.cluster_id): c.get_template() for c in miner.drain.clusters}
    with open(f"{out}/templates.json", "w") as f:
        json.dump(templates, f)
    rss = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
    if sys.platform == "darwin":
        rss //= 1024
    stats = {
        "lines": len(ids),
        "seconds": seconds,
        "max_rss_kib": rss,
        "python": platform.python_version(),
        "drain3": version("drain3"),
    }
    with open(f"{out}/stats.json", "w") as f:
        json.dump(stats, f)


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
