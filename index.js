const childProcess = require('child_process')
const os = require('os')
const process = require('process')
const path = require('path')

const ARGS = ''.split(',').filter(arg => arg !== '')
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
        return `main-windows-amd64.exe`
    }

    console.error(`Unsupported platform (${platform}) and architecture (${arch})`)
    process.exit(0)
}

function main() {
    // Skip all operations if not running on RunsOn runners
    if (!process.env.RUNS_ON_RUNNER_NAME) {
        console.log('Not running on RunsOn runner, skipping all operations')
        process.exit(0)
    }

    const binary = chooseBinary()
    const mainScript = path.join(__dirname, binary)
    if (os.platform() === WINDOWS) {
        console.log(`Starting ${mainScript} with arguments ${ARGS.join(' ')}`, ARGS.length)
        // runner user has elevated privileges, so we can just run the script directly
        childProcess.execFileSync(mainScript, ARGS, { stdio: 'inherit' })
    } else {
        try {
            childProcess.execFileSync('sudo', ['-n', '-E', mainScript, ...ARGS], { stdio: 'inherit' })
        } catch (error) {
            try {
                const whoami = childProcess.execSync('whoami').toString().trim()
                console.log(`Current user (whoami): ${whoami}`)
            } catch (error) {
                console.log('Could not determine user via whoami')
            }
            if (error.code === 'ENOENT') {
                // sudo not available (likely in container, which is already running as root), try running directly
                childProcess.execFileSync(mainScript, ARGS, { stdio: 'inherit' })
            } else {
                throw error
            }
        }
    }
    process.exit(0)
}

if (require.main === module) {
    main()
}