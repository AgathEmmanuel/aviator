# Aviator  

## Overview  

**Aviator** is a Kubernetes operator designed to enhance workload management by introducing **latency-based traffic routing**. 

****NOTE***: This is based on the paper [Load is not what you should balance: Introducing Prequal](https://arxiv.org/abs/2312.10172) and Aviator is a micro-implementation of Prequal created as a learning exercise.*

This operator ensures that:  
- Traffic is routed to the **least busy pods** based on latency.  

Aviator is ideal for latency-sensitive applications, such as real-time systems, gaming, financial trading platforms, or live-streaming services.  

---

## Features  

- **Latency-Driven Traffic Routing**: Routes traffic to the pod with the lowest latency.  
- **Easy Integration**: Works seamlessly with existing Kubernetes workloads like `Deployments` and `StatefulSets`.  
- **Customizable Thresholds**: Configure latency thresholds and ping intervals to suit your application needs.  

---

## Installation  

1. **Install the CRD**  
   ```bash  
   kubectl apply -f https://github.com/your-repo/aviator-operator/releases/latest/download/crd.yaml  

2. Deploy the Aviator Operator

   ```bash  
   kubectl apply -f https://github.com/your-repo/aviator-operator/releases/latest/download/operator.yaml  

## Usage

1. Define an Aviator Policy

   Create a manifest to define a latency-based policy for your workload:


   ```
   apiVersion: aviator.io/v1alpha1  
   kind: AviatorPolicy  
   metadata:  
     name: my-app-aviator-policy  
   spec:  
     targetRef:  
       apiVersion: apps/v1  
       kind: Deployment  
       name: my-app  
     latencyThreshold: 100ms  # Maximum acceptable latency  
     pingInterval: 5s        # Interval between latency checks  
   
   ```

   Apply the manifest:
   ```
   kubectl apply -f aviator-policy.yaml  
   ```

## Configuration  

- latencyThreshold: Maximum acceptable latency for traffic routing.
- pingInterval: Time between dummy ping requests to measure latency.

## How It Works

Latency Monitoring:  
The operator continuously pings all pods in the target workload to measure their latency.

Traffic Routing:  
Traffic is routed to the pod with the lowest latency using Kubernetes services or annotations for load balancers.


## Compatibility

- Kubernetes 1.20+
- Works with Deployment and StatefulSet



## Reference  
- [Load is not what you should balance: Introducing Prequal](https://arxiv.org/abs/2312.10172)  


