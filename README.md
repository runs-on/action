## RunsOn: self‐hosted runners made simple, fast, and cheap

The `runs-on/action` GitHub Action allows anyone with access to an AWS account to spawn ephemeral and very cheap self-hosted runners that can be used by your workflows.

This allows you to tactically use more powerful or specialized instances for workflows that cannot run on one of the provided GitHub runner types.

For instance, you can select the fastest single-CPU instances offered by AWS to make your test suite fly, or use high-memory or gpu-enabled instances for your ML workflows.

Once you taste the speed of `c7a.8xlarge` machines (32 CPUs, 64GB RAM) for $0.029/min (on-demand) or $0.012/min (spot pricing), you can't go back. A similar machine at BuildJet.com will cost you $0.048 / min (1.5x to 4x times as much). At GitHub (if you have access to it), it's a whopping $0.128 / min (4x to 10x as much).

Oh, and on those high-spec instances, the bandwidth at AWS is miles better than what you can get at other self-hosted providers. Which matters _a lot_ when downloading lots of stuff (including caches).

## Why this action is awesome

The **big differentiators** from alternatives are as follows:

* fully managed, fully isolated: the action takes care of launching a new machine for each workflow job, and the machine is automatically terminated after the job has completed. We also make sure that no dangling resource are left, since in all cases the machine will automatically destroy itself after 20 minutes if for some reason no job has been scheduled on it, and in all cases after 8h if the job hasn't finished by then.

* runs in your own AWS account. This means no one can see your code except you. And you can select the best machine type for your needs.

* we provide the exact same images than what GitHub provides in [github/runner-images](https://github.com/actions/runner-images), but built for AWS. This means your existing workflows can run without any changes. Images are built by [runs-on/runner-images-for-aws](https://github.com/runs-on/runner-images-for-aws) within 24h of their official release, and are automatically used by the action.

* more than 2x cheaper than the cheapest competition, for a similar or better performance: the most established self-hosted runner provider in the space is probably BuildJet.com, and since there is no intermediary between your workflows and the machines, you get the real cost of machines, while having a fully managed solution

* spot pricing: while being 2x cheaper than the competition is already nice, you can get even more savings by electing to use spot pricing for your runners. The action will automatically cycle through multiple instance types until it finds one available.

## The downside

You need to add a preliminary job to the workflows that need a self-hosted runner, which means some initial setup to declare a private GitHub App with the correct permission (yes, singular!), AWS access key creation, and about 20 lines in your workflow to configure the action and the kind of runner you want.

The action itself will add about 20s to 30s to your workflows.

## Supported operating systems

* ubuntu-22
* macos (soon)

## Initial setup

The required steps to enable the runs-on action are as follows:

* generate a github app with the "administration" repository permission, and select the repositories this action can run on.

![GitHub App permissions](https://github.com/runs-on/action/raw/main/doc/img/github-app-permissions.png)
![GitHub App repositories](https://github.com/runs-on/action/raw/main/doc/img/github-app-repositories.png)

* store the github app ID and PRIVATE KEY in the repository secrets as `RUNS_ON_APP_ID` and `RUNS_ON_APP_PRIVATE_KEY`

* store your AWS credentials in the repository secrets, using the official [aws-actions/configure-aws-credentials](https://github.com/aws-actions/configure-aws-credentials) action.

![GitHub App repositories](https://github.com/runs-on/action/raw/main/doc/img/repository-action-secrets.png)

* add the `runs-on/action` to your workflow (see example below)

* watch your CI fly!

## Example workflow file

```yaml
name: "Test RunsOn Action"

on:
  workflow_dispatch:

jobs:
  spawn_runner:
    name: Spawn beefy ephemeral AWS runner
    runs-on: ubuntu-latest
    outputs:
      runner-label: ${{ steps.runs_on.outputs.runner-label }}
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          aws-access-key-id: ${{ secrets.AWS_ACCESS_KEY_ID }}
          aws-secret-access-key: ${{ secrets.AWS_SECRET_ACCESS_KEY }}
          aws-region: us-east-1
      - uses: runs-on/action@v1
        id: runs_on
        with:
          github-app-id: ${{ secrets.RUNS_ON_APP_ID }}
          github-app-private-key: ${{ secrets.RUNS_ON_APP_PRIVATE_KEY }}
          runner-types: "c7a.large,c6a.large"
          runner-os: ubuntu22
          spot-instances: true

  main_job:
    name: Job that executes on self-hosted runner
    needs: spawn_runner
    runs-on: ${{ needs.spawn_runner.outputs.runner-label }}
    steps:
      - name: "Hello World"
        run: |
          echo "Hello from your self-hosted runner $HOSTNAME"
```

## Roadmap

* ~~spot instance support~~
* ~~cycle through instance types until one available~~
* ~~automatically terminate instance if no job received within 20min~~
* ~~automatically terminate instance if job not completed within 8h~~
* ~~allow to specify storage type, iops, size~~
* ~~allow repo admins to SSH into the runners~~
* allow to set max daily budget and/or concurrency
* macos support
* allow user-provided AMIs (need to make the user-data script a bit more clever)

## FAQ

Q: Why use a GitHub App instead of a GitHub Token for authentication?  
A: The default `${{ github.token }}` generated for each workflow cannot have administrative permission to create new runners from the workflow, so we either need a Personal Access Token (PAT), or a GitHub App ID and Private Key. The issue with PATs is that they are linked to a specific user, and have a default expiration date, which is more brittle than relying on a GitHub App.

Q: What software is installed on the runners?  
A: For Ubuntu22 x64 runners, we use the exact same tools than the official GitHub runners. So your workflows should work there without any changes.

Q: Can I use caches?  
A: Yes, and they are very fast to download/upload since AWS has a pretty crazy bandwidth (compared to other self-hosted providers). 

Q: Is this free to use?  
A: For non-commercial use, yes. For commercial use, it's currently free to try for 30 days. After that, there will be a one-time license fee to pay to support ongoing development costs and AMI generation and hosting costs. This part is still in flux but in any case I want this to be a no-brainer if you're currently paying for additional GitHub build minutes, or any other provider.
