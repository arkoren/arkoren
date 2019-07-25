import Log from '../app/http/middleware/log.ts'
import { Router } from '../../framework/http.ts'
import { MainController } from '../app/http/controllers/main.ts'

/**
 * Export the application routes.
 *
 * @export
 * @param {Router} route
 */
export function routes(route: Router) {
    route.get('/', MainController.index)
    route.get('/another', MainController.another, { middleware: [Log] })
    route.get('/book/:name/:page', MainController.book)
    route.group({ middleware: [Log] }, route => {
        route.get('/test', MainController.index)
    })
}
