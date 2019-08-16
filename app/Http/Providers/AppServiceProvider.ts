import { Provider } from '../../../../framework/http.ts'

/**
 * Route service provider.
 *
 * @export
 * @class AppServiceProvider
 * @extends {Provider}
 */
export class AppServiceProvider extends Provider {

    /**
     * Registers the provider to the service container.
     *
     * @memberof AppServiceProvider
     */
    register() {
        // ...
    }

    /**
     * This method is called after all other service providers
     * have been registered.
     *
     * @memberof AppServiceProvider
     */
    boot() {
        // ...
    }

}
