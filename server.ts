import { routes } from './routes/web.ts'
import { App } from '../framework/app.ts'
import { Kernel as http_kernel } from './app/http/kernel.ts'

const app = new App({
    host: '127.0.0.1',
    port: 9000,
    routes,
    http_kernel
})

app.start()
