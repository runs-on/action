const process = require('process');
const fs = require('fs');
const path = require('path');

async function computeExecutionCost() {
  // Get the display costs option value
  const displayCostsOption = process.env.INPUT_DISPLAY_COSTS || 'inline';

  // Disable if not 'inline' or 'summary'
  if (displayCostsOption !== 'inline' && displayCostsOption !== 'summary') {
    console.log(`Cost calculation is disabled (display-costs=${displayCostsOption})`);
    return;
  }

  const instanceLaunchedAt = process.env.RUNS_ON_INSTANCE_LAUNCHED_AT;
  
  if (!instanceLaunchedAt) {
    console.log('RUNS_ON_INSTANCE_LAUNCHED_AT environment variable not found. Cannot compute cost.');
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
        startedAt: instanceLaunchedAt
      }),
      signal: controller.signal
    });
    clearTimeout(timeoutId);

    if (!response.ok) {
      throw new Error(`HTTP error! Status: ${response.status}`);
    }

    const costData = await response.json();
    
    if (displayCostsOption === 'inline' || displayCostsOption === 'summary') {
      // Display in console as before
      console.log('\n===== Execution Cost Summary =====');
      console.log(`Instance Type: ${costData.instanceType}`);
      console.log(`Region: ${costData.region}`);
      console.log(`Duration: ${costData.durationMinutes} minutes`);
      console.log(`Cost: $${costData.totalCost}`);
      console.log(`GitHub equivalent cost: $${costData.github.totalCost}`);
      console.log(`Savings: $${costData.savings.amount} (${costData.savings.percentage}%)`);
      console.log('==================================\n');
    }
    if (displayCostsOption === 'summary') {
      // Add to GitHub job summary
      const summaryPath = process.env.GITHUB_STEP_SUMMARY;
      const summary = `
## Execution Cost Summary

| Metric | Value |
| ------ | ----- |
| Instance Type | ${costData.instanceType} |
| Region | ${costData.region} |
| Duration | ${costData.durationMinutes} minutes |
| Cost | $${costData.totalCost} |
| GitHub equivalent cost | $${costData.github.totalCost} |
| Savings | $${costData.savings.amount} (${costData.savings.percentage}%) |
`;
      fs.appendFileSync(summaryPath, summary);
      console.log('Cost summary added to job summary');
    }

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