# Dropoutbox service

I'm making a distributed, self-hosted file sharing and file replication service.  
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
2. Multi-directional replication where changes to one copy are propagated to others using type of the change (crud operation) replica id and a timestamp of the change for conflict resolution (this can lead to replication error due to unsolvable conflicts that require user intervention \- which is ok for time being, later I maybe introduce some additional rules for conflict resolution)   
3. One-way replication replicas can form a tree structure where any downstream replica can have only one source   
4. Multi-directional replica can only be at a base of a three but cannot an upstream source of the one-way replication type

### Share
* It's an inventory accessible through the web interface, depending on ownership and permissions and replication rules, end user can use a share to perform CRUD operation on the files accessible through the share  
* In case of conflicting permissions and rules (i.e. updateable share for read-only inventory) it's up to the service to detect conflicting settings and display an error  
* While share is defined for an inventory it actually uses replicas to work with the actual data and depending on the rules it can result to using multiple replicas for different actions (i.e. downstream read-only replica for fast data read and writeable replica for data update) 

### Users, ownership and permissions

* Users can be authenticated or anonymous. Inventory and share can have one or more users and each user can have a list of actions allowed to perform for an inventory or share.  
* Replicas do not have explicit user's permissions \- what can be done to an replica depends on an inventory permissions and replica type

## Deployment model

Even though this is a distributed service that works with the distributed data and scenarios of eventual consistency, it requires a designated main node that holds system state, assuming that the main node must be online and available for processes to work.

## System components

![System components](system_components.jpg)

### Coordinator service + database + admin UI

There is only one coordinator service main database holding system state, replication processes and sharing work when that service is online, otherwise everything waits

### Storage service

Part of the system that handles replica(s) on the filesystem or cloud storage. Each replica is handled by one storage service. Even in the case of cloud storage like s3, there is one storage service responsible for the replica. One storage service instance can manage multiple replicas on multiple locations.  
Storage service must have sufficient permissions and credentials to manage its replicas and must return appropriate error in case of permissions and/or credential problems.  
To avoid a split state scenario, the storage service should have as little local state as possible: it should publish all changes on the local replicas to the coordinator and ask for instructions on how to proceed. In case of the  unavailable coordinator, it should halt all replication until coordinator becomes available again.  
question: is it sufficient for storage service to have no persisted local state and maintain only in-memory state

### Data bus

This is maybe not an actual component but a set of connections established on demand between storage services to transfer data between storage services or between a storage service and a sharing service.  
question: how to make a data bus really \- one way is to use zerotier/tailscale to maintain virtual local network between nodes another is to use coordinator to establish direct connections

### Sharing service + sharing UI

Sharing service is both a web app with UI with data presentation (previews for images, links for documents etc) and interface for data upload / replace / delete if share permissions allow it.  
The sharing service uses the coordinator to resolve which replica to use and can even use local read-only replica for fast read and remote updateable replica for update.

## Database

![Database](database.jpg)

### Tables and fields descriptions

#### inventories

name \- if not specified, will use folder or file name  
status \- online, offline, deleted  
type \- file, folder

#### inventory\_files
relative\_uri \- file uri from the inventory root. replica uri \+ relative\_uri make full file pathfile\_journal  
version \- version corresponds to the file\_journal id for this file, having journal entry for the file with the journal id above the version value means there are changes to the file not yet processed  
status \- active, deleted

#### file\_journal
action \- crud action: created, updated, modified, deleted  
replica\_id \- replica on which action occurred  
version \- version on which action has been performed  
timestamp \- action timestampreplica\_id \- replica on which action occurred

#### replicas
node\_id \- id of the service node on which replica exists  
uri \- data prefix uri for the replica  
status \- active, deleted  
type \- storage, filesystem, removable

#### replica\_files
synchronized \- boolean, indicates local change that needs to be propagated in case multi-directional replication or overridden for read-only replica  
version \- last file version in the replica  
status \- changed, pending, synchronized, conflict, error:  
changed \- local change that needs to be propagated in the case of multi-directional replication or overridden in case of read-only replication  
pending \- waiting for remote changes to be applied to the local copy  
synchronized \- all changes reconciled, nothing to do  
conflict \- multiple changes detected, requires manual fix  
error \- problems other than conflict, for example permission problem

#### replication\_groups
type \- bi-directional, one-way  
status \- active, deleted

#### group\_replicas
upstream\_id \- optional upstream replica in case of one-way replication tree, nullable

#### shares
name \- if not specified, will use inventory name  
status \- active, deleted

#### users
name \- username or email    
status \- active, deleted  

#### user_roles

#### roles
status - active, deleted

#### permissions
resource - users, shares, inventories (permissions to manage inventories implies permission to manage replicas and replication groups)
action - read, create, update delete
