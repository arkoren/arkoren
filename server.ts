import app_config from './config/app.ts'
import { App } from '../framework/app.ts'
import { Kernel as http_kernel } from './app/Http/Kernel.ts'

const app = new App({
    host: '127.0.0.1',
    port: 9000,
    providers: app_config.providers,
    http_kernel
})

app.start()
