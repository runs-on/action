const process = require('process');

async function computeExecutionCost() {
  // Check if cost display is disabled
  if (process.env.INPUT_DISPLAY_COSTS === 'false') {
    console.log('Cost calculation is disabled (display-costs=false)');
    return;
  }

  const startedAt = process.env.RUNS_ON_JOB_STARTED_AT;
  
  if (!startedAt) {
    console.log('RUNS_ON_JOB_STARTED_AT environment variable not found. Cannot compute cost.');
    return;
  }

  // Get runner information from environment variables
  const region = process.env.RUNS_ON_AWS_REGION || '';
  const instanceType = process.env.RUNS_ON_INSTANCE_TYPE || '';
  const instanceLifecycle = process.env.RUNS_ON_INSTANCE_LIFECYCLE || 'spot';

  // get average price for the region for now
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 5000);
    const response = await fetch('https://ec2-pricing.runs-on.com/cost', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        instanceType,
        region,
        instanceLifecycle,
        startedAt
      }),
      signal: controller.signal
    });
    clearTimeout(timeoutId);

    if (!response.ok) {
      throw new Error(`HTTP error! Status: ${response.status}`);
    }

    const costData = await response.json();
    
    console.log('\n===== Execution Cost Summary =====');
    console.log(`Instance Type: ${costData.instanceType}`);
    console.log(`Region: ${costData.region}`);
    console.log(`Duration: ${costData.durationMinutes} minutes`);
    console.log(`Cost: $${costData.totalCost}`);
    console.log(`GitHub equivalent cost: $${costData.github.totalCost}`);
    console.log(`Savings: $${costData.savings.amount} (${costData.savings.percentage}%)`);
    console.log('==================================\n');

  } catch (error) {
    console.error('Error computing execution cost:', error);
  }
}

async function main() {
  await computeExecutionCost();
}

main().catch(err => {
  console.error(err);
  process.exit(1);
}); 