import { Router } from '../../arkoren/http.ts'
import { MainController } from '../app/http/controllers/main.ts'

/**
 * Export the application routes.
 *
 * @export
 * @param {Router} route
 */
export function routes(route: Router) {
    route.get('/', MainController.index)
    route.get('/another', MainController.another)
    route.get('/book/:name/:page', MainController.book)
    /*
    route.middleware(['a', 'b']).group(route => {
        route.get('/test', MainController.index)
    })
    */
}
