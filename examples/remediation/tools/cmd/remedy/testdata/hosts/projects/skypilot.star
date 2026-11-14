project(
    repo = "o/n",
    forge = "github",
    build_host = "lane",
    base_branch = "main",
    gate = "make check",
    test_report = "r.xml",
    host = host(
        provider = "skypilot",
        disk_floor_gb = 50,
        idle_minutes = 30,
        skypilot = skypilot(
            cloud = "gcp",
            region = "us-central1",
            instance_type = "n2-standard-8",
        ),
    ),
)
