import { routes } from './routes/web.ts'
import { App } from '../framework/app.ts'

new App("127.0.0.1", 9000)
    .routes(routes)
    .start()
