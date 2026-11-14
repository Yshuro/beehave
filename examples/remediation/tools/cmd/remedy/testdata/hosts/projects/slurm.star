project(
    repo = "o/n",
    forge = "github",
    build_host = "lane",
    base_branch = "main",
    gate = "make check",
    test_report = "r.xml",
    host = host(
        provider = "slurm",
        disk_floor_gb = 50,
        slurm = slurm(
            address = "head.example",
            user = "ci",
            port = 2222,
            identity = "/keys/ci",
            work_root = "/home/ci/work",
            known_hosts = "accept-new",
        ),
    ),
)
