import { App } from '../arkoren/app.ts'
import { routes } from './routes/web.ts'

const app = new App("127.0.0.1", 9000)
app.routes(routes)
app.start()
