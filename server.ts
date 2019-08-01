import config from './config/app.ts'
import { App } from '../framework/app.ts'
import { Kernel as http_kernel } from './app/Http/Kernel.ts'

/**
 * The main entry point for the application.
 *
 * @returns void
 */
async function main() {
    const app = new App({ ...config, http_kernel })
    await app.start()
}

main()
