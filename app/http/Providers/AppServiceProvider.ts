import { Provider } from '../../../../framework/http.ts'

/**
 * Route service provider.
 *
 * @export
 * @class RouteServiceProvider
 * @extends {Provider}
 */
export class AppServiceProvider extends Provider {

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
        // ...
    }

}
