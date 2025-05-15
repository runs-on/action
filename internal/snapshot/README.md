# RunsOn snapshotting feature

When you include this GitHub Action into your workflow job steps, it will:

1. attempt to find an existing EBS volume tagged with the current branch name (tag key: `runs-on-snapshot-branch`).
1a. if no volume exists, create a new volume with size 60GB, type=gp3, provisioned throughput=700, and attach it to the current instance
1b. if a volume exists, clone it and attach the new volume to the instance, as in 1a.
2. Once the volume is attached, automatically mount it at `/var/lib/docker` (stop the docker daemon first)
3. Provide a function to detach the volume.


Open Questions:

1. what is the fastest / best way to accomplish this?
2. do we need to create a snpashot first before cloning a volume?
3. can we somehow trigger the creation of a snapshot asynchronously at the end of the job, without waiting for it, so that it is available for the next job?