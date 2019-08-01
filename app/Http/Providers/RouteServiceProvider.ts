import { Provider } from '../../../../framework/http.ts'
import { routes } from '../../../routes/web.ts'

/**
 * Route service provider.
 *
 * @export
 * @class RouteServiceProvider
 * @extends {Provider}
 */
export class RouteServiceProvider extends Provider {

    /**
     * Registers the provider to the service container.
     *
     * @memberof RouteServiceProvider
     */
    register() {
        // ...
    }

    /**
     * This method is called after all other service providers
     * have been registered.
     *
     * @memberof RouteServiceProvider
     */
    boot() {
        this.app
            .router()
            .group({ middleware: 'web' }, routes)
    }

}
