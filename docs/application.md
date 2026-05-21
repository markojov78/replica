# DropOuBbox service

A distributed, self-hosted file sharing and file replication service.  
While the initial idea for the service is to facilitate storage, backup and sharing of my own photo collection, it is not limited to specific data type: storage and replication functionality should be agnostic to data type and frontend is intended to be extensible to present different file types (images, audio, video, documents ...)

These are the main functionalities that the service offers:

* File sharing through the web interface  
* File replication trough rules

## Key concepts

### Inventory
* Inventory entry is either individual file or a folder holding a collection of files and folders (i.e. photos)  
* Inventory can have replication rules, sharing rules, ownership and permissions.  
* Inventory can be created either for replication and/or for sharing but neither is mandatory on an inventory  
* Inventories can overlap: a file or folder can be part of multiple inventories with different replication rules, shares and permissions.

### Replica
* Each inventory has at least one replica which points to the physical location of data.  
* Replica can be defined on local computer storage, cloud storage (i.e. aws s3, with ability to add support for more storage types) and removable storage, like external disk occasionally plugged into the computer to receive data backup  
* An inventory can have multiple replicas with the following replication rules:   
1. One-way replication (source to replica)where all changes in the source are propagated to the destination and all the discrepancies are solved by making destination identical to the source   
2. Multi-directional replication where changes to one copy are propagated to others using type of the change (crud operation) replica id and a timestamp of the change for conflict resolution (this can lead to replication error due to unsolvable conflicts that require user intervention - which is ok for time being, later I maybe introduce some additional rules for conflict resolution)   
3. One-way replication replicas can form a tree structure where any downstream replica can have only one source   
4. Multi-directional replica can only be at a base of a three but cannot an upstream source of the one-way replication type

### Share
* It's an inventory accessible through the web interface, depending on ownership and permissions and replication rules, end user can use a share to perform CRUD operation on the files accessible through the share  
* In case of conflicting permissions and rules (i.e. updatable share for read-only inventory) it's up to the service to detect conflicting settings and display an error  
* While share is defined for an inventory it actually uses replicas to work with the actual data and depending on the rules it can result to using multiple replicas for different actions (i.e. downstream read-only replica for fast data read and writeable replica for data update) 

### Users, ownership and permissions
* Users can be authenticated or anonymous. Inventory and share can have one or more users and each user can have a list of actions allowed to perform for an inventory or share.  
* Replicas do not have explicit user's permissions - what can be done to an replica depends on an inventory permissions and replica type

## Deployment model
Even though this is a distributed service that works with the distributed data, the main design goal is data integrity 
over availability, so it requires a designated coordinator node that holds the system state.  
All other nodes need the coordinator to be online and available for service to work.

## System components
![System components](system_components.jpg)

### Coordinator service + database + API + admin UI
There is only one coordinator service main database holding system state and exposing 
[public API](api.md#public-api) for administration and [internal API](api.md#internal-api) for node coordination.

### Storage service
Part of the system that handles replica(s) on the filesystem or cloud storage. 
Each replica is handled by one storage service. Even in the case of cloud storage like s3, there is one storage service 
responsible for the replica. One storage service instance can manage multiple replicas on multiple locations.  
Storage service must have sufficient permissions and credentials to manage its replicas and must return appropriate 
error in case of permissions and/or credential problems.  
To avoid a split state scenario, the storage service will have no persisted state: it will authenticate with the 
coordinator, and then retrieve the state from the coordinator.
IT will peridocialy scan replicas and report changes to the coordinator, and ask for instructions on how to proceed. 
In case of the unavailable coordinator, it should halt all replication until the coordinator becomes available again.  

### Data bus
This is maybe not an actual component but a set of connections established on demand between storage services to transfer 
data between storage services or between a storage service and a sharing service.  
question: how to really make a data bus:  one way is to use zerotier/tailscale to maintain virtual local network between nodes another is to use coordinator to establish direct connections

### Sharing service + sharing UI
Sharing service is both a web app with UI with data presentation (previews for images, links for documents etc) and interface for data upload / replace / delete if share permissions allow it.  
The sharing service uses the coordinator to resolve which replica to use and can even use local read-only replica for fast read and remote updateable replica for update.

## Database

![Database](database.jpg)

### Tables and fields descriptions

#### nodes
status - online, unreachable, offline, disabled, revoked
secret - hashed secret for node to coordinator authentication 
address - node address reported to the coordinator
last_seen - last time the node reported to the coordinator
last_callback_success - last time the node replied to the coordinator callback
last_callback_failure - last time the node filed to reply to the coordinator callback

#### inventories
name - if not specified, will use folder or file name  
status - online, offline, deleted  
type - file, folder

#### inventory_files
relative_uri - file uri from the inventory root. replica uri + relative_uri make full file pathfile_journal  
version - version corresponds to the file_journal id for this file, having journal entry for the file with the journal id above the version value means there are changes to the file not yet processed  
status - active, deleted

#### file_journal
action - crud action: created, updated, modified, deleted  
replica_id - replica on which action occurred  
version - version on which action has been performed (old version)  
timestamp - action timestamp  
replica_id - replica on which action occurred  

#### replicas
node_id - id of the service node on which replica exists  
uri - data prefix uri for the replica  
status - active, deleted  
type - storage, filesystem, removable

#### replica_files
synchronized - boolean, indicates local change that needs to be propagated in case multi-directional replication or overridden for read-only replica  
version - last file version in the replica  
status - changed, pending, synchronized, conflict, error:  
changed - local change that needs to be propagated in the case of multi-directional replication or overridden in case of read-only replication  
pending - waiting for remote changes to be applied to the local copy  
synchronized - all changes reconciled, nothing to do  
conflict - multiple changes detected, requires manual fix  
error - problems other than conflict, for example permission problem

#### replication_groups
type - bi-directional, one-way  
status - active, deleted

#### group_replicas
upstream_id - optional upstream replica in case of one-way replication tree, nullable

#### shares
name - if not specified, will use inventory name  
status - active, deleted

#### users
name - username or email    
status - active, deleted  

#### user_roles

#### roles
status - active, deleted

#### permissions
resource - users, shares, inventories (permissions to manage inventories implies permission to manage replicas and replication groups)
action - read, create, update delete

## Operation
### Communication between nodes
TODO

### Creating a new inventory
When an inventory is created, the coordinator creates the logical inventory record and its first/default replica. 
The default replica is the initial physical location from which the inventory content is discovered. 

#### 1) User requests inventory creation
For example:  
inventory name: Photos  
inventory type: folder  
default replica node: A  
default replica uri: /data/photos  
replica type: filesystem  

#### 2) Coordinator inserts `inventories`
```
inventories
id  name    type    status
--------------------------
1   Photos  folder  active
```
This says:  
Inventory 1 exists as a logical dataset.  

#### 3) Coordinator inserts default replicas
```
replicas
id  inventory_id  node_id  uri           type        status
------------------------------------------------------------
A   1             node-1   /data/photos  filesystem  active
```
This says:  
Replica A is the first physical location for inventory 1.  

At this point, no files have necessarily been indexed yet.
#### 4) Storage service scans default replica
The storage service responsible for replica A scans `/data/photos`  
For every discovered file, it calculates: relative_uri , size, hash, modified (timestamp)   
Example discovered files:  

| relative_uri     | size | hash                             |
|------------------|------|----------------------------------|
| img001.jpg       | 125  | 7d35803cfcca9f0e046d30b5338efbab |
| album/img002.jpg | 256  | d5bddda567cc62b99e5695704a399c6a |

#### 5) Coordinator inserts `inventory_files`
For each discovered file:  
```
inventory_files
file_id  inventory_id  relative_uri       version  status  modified  size  hash
-----------------------------------------------------------------------------------------------------------
10       1             img001.jpg         1        active  time_1    125   7d35803cfcca9f0e046d30b5338efbab
11       1             album/img002.jpg   1        active  time_2    256   d5bddda567cc62b99e5695704a399c6a
```
This says:  
These are the authoritative logical files currently known for the inventory.  
Each starts at version 1.  

#### 6) Coordinator inserts `inventory_journal`
For each discovered file, insert a creation event:  
```
inventory_journal
id   file_id  inventory_id  replica_id  version  action   timestam
--------------------------------------------------------------------
101  10       1             A           0        created  time_1  
102  11       1             A           0        created  time_2  
```
Version in `inventory_journal` is the old version on which action has been performed, 
and version 0 here means that the file did not exist before this creation event.  

#### 7) Coordinator inserts `replica_files`
For each discovered file on default replica A:  
```
replica_files
file_id  replica_id  version  status
------------------------------------------
10       A           1        synchronized
11       A           1        synchronized
```
This says:  
Replica A already has the current version of these files.  

#### 8) Final state after inventory creation
```
inventories
id  name    type    status
--------------------------
1   Photos  folder  active

replicas
id  inventory_id  node_id  uri           type        status
------------------------------------------------------------
A   1             node-1   /data/photos  filesystem  active

inventory_files
file_id  inventory_id  relative_uri       version  status  modified  size  hash
-----------------------------------------------------------------------------------------------------------
10       1             img001.jpg         1        active  time_1    125   7d35803cfcca9f0e046d30b5338efbab
11       1             album/img002.jpg   1        active  time_2    256   d5bddda567cc62b99e5695704a399c6a

replica_files
file_id  replica_id  version  status
------------------------------------------
10       A           1        synchronized
11       A           1        synchronized
```

#### Important note
During inventory creation, the default replica is not `pending`.  
It is the source from which the initial inventory state is built.  
  
So the default replica starts as `synchronized`, and additional replicas added later start as `pending` 
because they need to receive the already-known inventory files.  

### File replication after update
When multiple replicas exist, this is how replication works:  
1. Detect local change on replica A
2. Record authoritative logical change
3. Mark other replicas as needing update
4. Transfer file data
5. Mark target replica synchronized

Concrete table usage:  
#### 1) Initial state
```
inventory_files
file_id  version  status  modified  size      hash
------------------------------------------------------
10       3        active  old_time  old_size  old_hash

replica_files
file_id  replica_id  version  status
------------------------------------------
10       A           3        synchronized
10       B           3        synchronized
```
#### 2) Storage service on replica A detects file changed
It calculates: new_hash , new_size , modified_time  
Then reports this to coordinator.  

#### 3) Coordinator updates `inventory_files`
```
inventory_files
file_id  version  status  modified  size      hash
------------------------------------------------------
10        4       active  new_time  new_size  new_hash
```
This says:  
The authoritative current version of this file is version 4.  

#### 4) Coordinator inserts `inventory_journal`
```
inventory_journal
id   file_id  inventory_id  replica_id  version  action   timestamp
-------------------------------------------------------------------
101  10       1             A           3        updated  new_time 
```

#### 5) Coordinator updates `replica_files`
```
replica_files
file_id  replica_id  version  status
------------------------------------------
10       A           4        synchronized
10       B           3        pending
```
This says:  
A has the current version.  
B still has an old version and needs update.  

#### 6) Replication worker finds pending target
It queries:  
`replica_files where status = pending`  
Then compares:  
`replica_files.version < inventory_files.version`  
So it knows:  
`copy file_id=10 version=4 to replica B`  
Source can be replica A, or any synchronized replica with version 4.  

#### 7) File data is transferred
Storage service copies actual data:  
replica A path -> replica B path  
  
Then verifies:  
`hash == inventory_files.hash && size == inventory_files.size`  

#### 8) Coordinator marks replica B synchronized
Final state after successful transfer:  
```
replica_files
file_id  replica_id  version  status
------------------------------------------
10       A           4        synchronized
10       B           4        synchronized

inventory_files
file_id  version  status  modified  size      hash
------------------------------------------------------
10        4       active  new_time  new_size  new_hash
```
