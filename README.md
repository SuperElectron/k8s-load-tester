# k8s-load-tester

Load-test a real multi-service stack on Kubernetes, without needing a real app to test against.

Spin up a client, API, and database on a local cluster, aim a load generator at it, and watch how it holds up — resource limits, autoscaling, ingress, connection pressure.

## What's deployed

- **React frontend:** Uses react query to load data from an api and display the result.
- **Go API:** Has `/` and `/ping` endpoints. `/` queries the Database for the current time and the number of requests for each api recorded within the database, and `/ping` returns `pong`.
- **Postgres Database:** An empty PostgreSQL database with no tables or data. Used to show how to set up connectivity. The API applications execute `SELECT NOW() as now;` to determine the current time to return.
- **Python Load Generator:** Queries the Go API at a configurable rate, set by `DELAY_MS` between requests.

![](./docs/screenshot.png)


## Running the Application

__set up__

Before we get started, you will need to download and install devbox - which manages all system dependencies.

- [install devbox](https://www.jetify.com/devbox/docs/installing_devbox/) 

---

__overview__

To keep things consistent, we have commands in the Taskfile.yaml for each folder to tell you exactly how to run it.

There are two ways to run. 
1. **Docker** is for local development AND you need to build and push the images. View [src/Taskfile.yaml](src/Taskfile.yaml).
2. **Kubernetes** is for deployment, uses KinD (kubernetes in Docker) for local or AWS for cloud.  View [helm/Taskfile.yaml](helm/Taskfile.yaml).

__use your own images__

Images push to my Dockerhub org, `superelectron`. To use your own, replace it in:

- `image.repository` — `helm/{api-golang,client,load-generator}/values.yaml`
- `DOCKER_REPO` — `helm/Taskfile.yaml`
- `IMAGE_REPO` — `src/{api-golang,client,load-generator,postgresql}/Taskfile.yaml`
- `image:` — `src/docker-compose.yml` and `src/*/Tiltfile`

__initiate shell__

```bash
# start the shell session
$ devbox shell
Starting a devbox shell...

```

__build docker images__

Now, let's build those docker containers.

```bash
# go into the src directory to build the docker images
cd src

# list the tasks (gives comprehensive list, we only care about the commands below)
task --list-all

# build and push
task build

# clean up the work, and release all references
task clean
task clean_deep

# go back to root directory
cd ..
```


__run with kubernetes__

And, we can deploy locally using KinD, to run the kubernetes cluster

```bash
# go into Kubernetes directory
cd helm

# create the KinD cluster
# this runs cloud-provider-kind in the foreground, so leave it running
task prepare

# in a second terminal, deploy the whole stack
task start

# start the load test
task load-test

# when you are done, tear it all down
# you can kill the first terminal after this
task stop
task delete
```

The cluster and namespace are both named `k8s-load-tester`. Override either without
editing the files:

```bash
CLUSTER_NAME=my-cluster task cluster:start
T_NAMESPACE=my-ns task namespace
```

---
