# dupop ( Duplicate Operation )  

A system that offers a Workflow similar to Crossplane but more configurable, customisable and fits into the Terraform ecosystem resulting in Operations becoming a one time task and duplication of those Operations across applications and processes much easier  


We will have standard sample templates for  

applications  
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
