# Aviator Docs  

## Implementation Steps  

### Install Kubebuilder  
```
curl -L -o kubebuilder https://go.kubebuilder.io/dl/latest/${GOOS}/${GOARCH}
chmod +x kubebuilder && mv kubebuilder /usr/local/bin/
```


### Scaffold a New Project
```
mkdir aviator && cd aviator
kubebuilder init --domain example.com --repo aviator
```

### Create the Custom Resource Definition (CRD)

Define the AviatorPolicy CRD.

```
kubebuilder create api --group aviator --version v1alpha1 --kind AviatorPolicy

```

This creates API files in api/v1alpha1/aviatorpolicy_types.go. Modify the AviatorPolicy struct to define our CRD fields  


Run the following to generate Kubernetes code:

```
make generate
make manifests
make generate-yaml
```


### Implement the Controller

Modify controllers/aviatorpolicy_controller.go to:


Watch for changes to the Service and its endpoints.
Periodically probe all pods behind the Service and measure latency.
Measure response latency
Identify the least-latency pod(s) and update the Service's endpoints to direct traffic accordingly.

Key Components of the Controller Logic

```
make install
make deploy
```

When using microk8s for local develepment, set the controller deployments imagePullPolicy to Never, so that it make use of the local registry  

Adding steps to Makefile to save the image from nerdctl images to microk8s image registry  

```
CONTAINER_TOOL ?= nerdctl


.PHONY: docker-save
docker-save: ## Save and import docker image with the manager.
	$(CONTAINER_TOOL) save ${IMG} > ${IMG}.tar
	microk8s ctr image import ${IMG}.tar
	rm ${IMG}.tar

```
Adding steps to generate yamls using kustomize  

```
.PHONY: generate-yaml
generate-yaml: manifests generate kustomize ## Generate all necessary YAML files for deployment.
	mkdir -p generated-yaml
	$(KUSTOMIZE) build config/crd > generated-yaml/crd.yaml
	$(KUSTOMIZE) build config/default > generated-yaml/default.yaml
```

### Add support for Monitoring & Observability  




## Development Workflow 

### Local Setup  

```
# To run the controller  
make run

# To run the controller and watch for file changes so that it reruns  
watchexec -r -e go -- go run ./cmd/main.go

```

### Cluster Setup  

Make use of tilt  


## Functional Testing  

#### Manual Testing  
Tested manualy by similating a practical scenario.  

```
nerdctl build -t test-app .

nerdctl save "$1" > "$1".tar && microk8s ctr image import "$1".tar && rm "$1".tar



```

#### Unit Testing  
Verifies individual components or modules of the software  


#### Integration Testing  
Tests the interaction between different modules or components.  

#### System Testing  

Tests the entire software system as a whole  




## Load Testing  

Simulating the load to test proper working  


## E2E Testing  

Tested with a real world application in Google Cloud GKE cluster  



## Troubleshooting  

```
Error
microk8s.kubectl logs testnginx -n aviator-system
Error from server: Get "https://192.168.1.15:10250/containerLogs/aviator-system/testnginx/testnginx": tls: failed to verify certificate: x509: certificate is valid for 192.168.1.11, not 192.168.1.15


Solution

1. Check Kubelet's Advertised IP

Run the following command to check the advertised node IP:

microk8s.kubectl get nodes -o wide

If the node is still using 192.168.1.11, but your request is going to 192.168.1.15, update your kubeconfig or rejoin the node.

2. Regenerate the Certificates

If the node's IP has changed, you need to regenerate the certificates:

microk8s stop
microk8s refresh-certs
microk8s start


Then check logs again
```







## Paper Summary  

- goal is to decrease latency  
- ignoring CPU utilization as a primary indicator of load  
- solution relies on finding the lowest latency based on a probe  
- selects the minimum latency from the set of probed services  
- two signal: Request in flight & Latency  
- leverages async probing  
- algorithm to find minimum is O(1)  
- used in client server load balancing  
- results: reduction of 2x in tail latency, 5-10 x in tail RIF, 10-20% in tail memory usage, 2x in tail CPU utilization, near elimination of errors due to load imbalance  
- probing to reduce queue latency  
- a load balancing policy implemented for grpc services of multi tenant systems ( youtube, search ads system, log processing )  
- usual policy is WRR ( Weighted Round Robin ) which is only used for GRPC servers  
- Weight calculation in WRR = Qi/Ui = (Queries Per Second / CPU utilization) => (0-100)  
- the higher the weight the better  
- load balancer gets cpu utilization from servers only after processing request  
- when LB sents request based on previous cpu utilization the server can choke  
- cpu utilization is the trailing signal  
- load of server measurment should use metrics as realtime as possible  
- prequal uses probes for fetching realtime load  
- client or LB probes server replicas  
- probing can be syncronous (probes before processing the request) or asynchronous (probes continously and stores the response of each probe randomly, and when the client gets a request instead of probing again and waiting for the response, it just choses the bes pobe in the probe pool which the client store and forward request to the server)  
- probe contains infos: server replica id, load signals(RIF, latency), receipt time  
- probes are managed and evicted in clients  
- clients decides the frequency of probing based on incoming trafffic  
- when client is not getting any requests, it will evict expired server probes  
- typical size of probe pool is 16 , and evict the worst probes   
- Replica selection  
- in youtube lets say a server got 100 requests, each request has to get a vedio from a database  
- hot cold lexicographic (HCL) selection: when the clients in continously probing the servers, it collects resulting probes in probe pool, then apply HCL rule, categorize service into hot and cold, its done by deciding what is a RIF level that corresponds to some percentile of servers, so if you are the hottest 20% of the servers in the system, you are deemed as hot server, and is exluded from selection and pick from the cold servers on with the lowest latency estimate, so the probes are feeding back both the RIF and latency estimate and pick the one with the lowest.  Client will not chose a hot service ( the service which might already be serving requests near to its threshold limit)
- Life of a probe: 
  - replace or remove  
  - pool capacity ( default 16 )
  - remove overused  
  - remove oldest  
  - remove worst  
    - query burst can lead to many probes in pool being used, even worst ones  
    - perserves power of d choices guarantees when reusing probe pool
    - flushes loaded servers from pool, whose probes are not used up  
- client choses the coldest probe where the latency is less  
- testbed: Load Ramp environment - Prequal vs WRR latency, when we ramp up the low from 75% to 175%, below 95% the latecy if similar for both, but after 95%, WRR performs terribly, while prequal is still able to maintain the tail latency standard, so what happens is that WRR keeps trying to keep the cpu load equal and it runs into those machines where there is no further cpu to go around, because they run jobs on it and gets high latency on those machines, but its very good at keeping with cpu utilization equal, whereas prequal will just rebalance into those machines that have spare capacity for other job  
- Prequal is about balancing latency, WRR is about balancing load  
- ideal probe rate: 2, 3 probes per query  
- Latency vs RIF based control  
- power of d choices paradigm  
- asynchronous background probes, at avg rate ~3 probes/query, query is not blocked waiting for probes  
- RIF: servers Request in flight, no of requests that have landed but not exited, so whenever a request comes counter is incremeneted and decremented when it exits  
- L(r)= t(finish) - t(start) : how long did the request take to process in the server. 
- probe reuse/removal: 
  - staleness: 
    - aging
    - overuse (increment of RIF value)
    - set a TTL
  - degradation: 
    - lightly loaded replicas are chosen all the time  
    - remove worst propes periodically  
    - one which is ages or one which is hot  
  - depletion:
    - to decrease probing rate, TTL & reuse count should be maintained  
    - even within TTL, if its used too many times, we remove it  
    - maintain balance b/w TTL & reuse count  
- error aversion to avoid sinkholding  
  - when an erroneous server is processing many request in small time  
  - it can attract much traffic into it  
  - LB should be smart  
- syncronous probing:
  - no probe pool  
  - for every request, we call for d probes, wait for d-1 probes  
  - chose the best one with provided mechanisms  
  - a small overhead to wait for probe response, typically takes 1ms  
