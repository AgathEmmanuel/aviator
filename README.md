# aviator 

A Project focused on learning K8s operator development in golang  

A system that offers a Workflow similar to Crossplane but more configurable, customisable and fits into the Terraform ecosystem resulting in Operations becoming a one time task and duplication of those Operations across applications and processes much easier  

Challenges:  

- all setups in a platform is directly dependend on the business logic  

Key points:

- a solution similar to that offered by crossplane but more customizable  
- a centralized kubernetes operator for the entire company to control everything except business logic  
- it will manage infra, ops, iams, monitoring, scaling, reliability etc.  
- functionality define any operation support required by individual business applications to be deployed  
- take care of the state management of the entire system from code hosting repository  
- everything will be recorded and state will be recorded as code  
- the entire thing can be spinned back up from the single source of truth  

A way to easily create, manage, control and operate:


applications  
cron jobs
other jobs on trigger
acesses  
infrastructure  
processes

all of which will be available as CRDs  

/dev-crds  
/ops-files  

Adding dev-crds with a flag will generate files in the ops-files folder  
if the flag is not present the ops-files are not generated as part of the repository  
but will be generated only in the dupop-operators local file system  
from which terraform or kubectl processes will apply those with a control loop mechanism  
dupop-engine have the capacity to generate the entire infra & apps from dev-crds  
