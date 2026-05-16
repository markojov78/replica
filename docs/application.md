# Application

The API binary can run in two deployment modes driven by config:

* coordinator + storage node
* storage-only node

Coordinator-enabled nodes initialize and migrate the database. Storage-only nodes connect to the database and expose the same HTTP process surface for future storage coordination endpoints.
