const childProcess = require('child_process')
const os = require('os')
const process = require('process')
const path = require('path')

const ARGS = '{{ .Args }}'.split(',').filter(arg => arg !== '')
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
    const binary = chooseBinary()
    const mainScript = path.join(__dirname, binary)
    if (os.platform() === WINDOWS) {
        console.log(`Starting ${mainScript} with arguments ${ARGS.join(' ')}`, ARGS.length)
        childProcess.execFileSync('powershell', [
            '-Command',
            `Start-Process -FilePath "${mainScript}" ${ARGS.length > 0 ? '-ArgumentList "' + ARGS.join(' ') + '"' : ''} -Verb RunAs -WindowStyle Hidden -Wait`
        ], { stdio: 'inherit' })
    } else {
        console.log(`Current user: ${process.env.USER || process.env.USERNAME}`)
        try {
            childProcess.execFileSync('sudo', ['-n', '-E', mainScript, ...ARGS], { stdio: 'inherit' })
        } catch (error) {
            if (error.code === 'ENOENT') {
                // sudo not available (likely in container), try running directly
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