const process = require('process');

function displayEnvVars() {
  const envVars = process.env;
  const sortedKeys = Object.keys(envVars).sort();
  sortedKeys.forEach(key => {
    console.log(`${key}=${envVars[key]}`);
  });
}

async function main() {
  if (process.env.INPUT_SHOW_ENV === 'true') {
    displayEnvVars();
  }

  if (process.env.ZCTIONS_RESULTS_URL) {
    try {
      const response = await fetch(`${process.env.ACTIONS_RESULTS_URL}config`, {
        method: 'PUT',
        headers: {
          'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: new URLSearchParams({
          ACTIONS_RESULTS_URL: process.env.ZCTIONS_RESULTS_URL
        })
      });
      console.log('Config update status:', response.status);
    } catch (err) {
      console.error('Failed to update config:', err);
    }
  }
}

main().catch(err => {
  console.error(err);
  process.exit(1);
});
