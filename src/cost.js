const { CostExplorerClient, GetCostAndUsageCommand } = require("@aws-sdk/client-cost-explorer");

// Create an instance of the CostExplorerClient
const client = new CostExplorerClient();

// Define parameters for the GetCostAndUsage command
const params = {
  TimePeriod: {
    Start: '2023-12-01', // Replace with your desired start date
    End: '2023-12-08',   // Replace with your desired end date
  },
  Granularity: 'DAILY',
  Metrics: ['BlendedCost'],
  Filter: {
    Tags: {
      Key: "stack",
      Values: ["runs-on"],
    },
  },
};

// Call the GetCostAndUsage command to retrieve cost and usage data
const getCostAndUsageCommand = new GetCostAndUsageCommand(params);

client.send(getCostAndUsageCommand)
  .then((data) => {
    console.log("Cost and Usage Data:", data);
    const { ResultsByTime } = data;
    for (const result of ResultsByTime) {
      console.log(`${result.TimePeriod.Start} - ${result.TimePeriod.End}: ${result.Total.BlendedCost.Amount}}`);
      for (const group of result.Groups) {
        console.log(`  ${group.Keys}: ${group.Metrics.BlendedCost.Amount}`);
      }
    }
  })
  .catch((error) => {
    console.error("Error fetching cost data:", error);
  });
