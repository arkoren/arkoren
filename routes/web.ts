import { Router } from '../../framework/http.ts'
import { Log } from '../app/Http/Middleware/Log.ts'
import { MainController } from '../app/Http/Controllers/MainController.ts'

/**
 * The application web router.
 *
 * @export
 */
export function routes(route: Router) {
    /**
     * You can register any application route in this function.
     * The middleware group called 'web' will be automatically
     * assigned to all of the following routes.
     */
    route.get('/', MainController.index)
    route.get('/another', MainController.another, { middleware: [Log] })
    route.get('/book/:name/:page', MainController.book)
    route.group({ middleware: [Log] }, route => {
        route.get('/test', MainController.index)
    })
}
