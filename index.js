const childProcess = require('child_process')
const os = require('os')
const process = require('process')

const ARGS = ''.split(',')

function chooseBinary() {
    const platform = os.platform()
    const arch = os.arch()

    if (platform === 'linux' && arch === 'x64') {
        return `main-linux-amd64`
    }
    if (platform === 'linux' && arch === 'arm64') {
        return `main-linux-arm64`
    }
    if (platform === 'windows' && arch === 'x64') {
        return `main-windows-amd64`
    }

    console.error(`Unsupported platform (${platform}) and architecture (${arch})`)
    process.exit(1)
}

function main() {
    const binary = chooseBinary()
    const mainScript = `${__dirname}/${binary}`
    console.log('Current user:', childProcess.execFileSync('sudo', ['-n', '-E', 'whoami']).toString().trim())
    
    // Create a simple askpass script that returns empty password
    // const askpassScript = `${__dirname}/askpass.sh`
    // fs.writeFileSync(askpassScript, '#!/bin/sh\necho ""', { mode: 0o755 })
    
    // // Set SUDO_ASKPASS and use -A flag
    // process.env.SUDO_ASKPASS = askpassScript
    childProcess.execSync([mainScript, ...ARGS].join(' '), { stdio: 'inherit' })
    process.exit(0)
}

if (require.main === module) {
    main()
}