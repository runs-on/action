const childProcess = require('child_process')
const os = require('os')
const process = require('process')
const path = require('path')

const ARGS = '{{ .Args }}'.split(',')
const WINDOWS = 'win32'
const LINUX = 'linux'
const AMD64 = 'x64'
const ARM64 = 'arm64'

function chooseBinary() {
    const platform = os.platform()
    const arch = os.arch()

    if (platform === LINUX && arch === AMD64) {
        return `main-linux-amd64`
    }
    if (platform === LINUX && arch === ARM64) {
        return `main-linux-arm64`
    }
    if (platform === WINDOWS && arch === AMD64) {
        return `main-windows-amd64`
    }

    console.error(`Unsupported platform (${platform}) and architecture (${arch})`)
    process.exit(1)
}

function main() {
    const binary = chooseBinary()
    const mainScript = path.join(__dirname, binary)
    
    const spawnSyncReturns = childProcess.spawnSync(mainScript, ARGS, { 
        stdio: 'inherit' 
    })
    
    // Handle spawn errors
    if (spawnSyncReturns.error) {
        console.error(`Failed to execute ${binary}:`, spawnSyncReturns.error.message)
        process.exit(1)
    }
    
    // Exit with child process status, defaulting to 1 if null/undefined
    process.exit(spawnSyncReturns.status ?? 1)
}

if (require.main === module) {
    main()
}