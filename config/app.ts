import { isProvider } from '../../framework/http.ts'
import { AppServiceProvider } from '../app/Http/Providers/AppServiceProvider.ts'
import { RouteServiceProvider } from '../app/Http/Providers/RouteServiceProvider.ts'

export default {

    /*
    |--------------------------------------------------------------------------
    | Application Host and Port
    |--------------------------------------------------------------------------
    |
    | These values are used to bind the HTTP application to a host and a port.
    |
    */
    host: <string> '127.0.0.1',
    port: <number> 9000,

    /*
    |--------------------------------------------------------------------------
    | Application Name
    |--------------------------------------------------------------------------
    |
    | This value is the name of your application. This value is used when the
    | framework needs to place the application's name in a notification or
    | any other location as required by the application or its packages.
    |
    */
    name: <string> 'My Application',

    /*
    |--------------------------------------------------------------------------
    | Application Environment
    |--------------------------------------------------------------------------
    |
    | This value determines the "environment" your application is currently
    | running in. This may determine how you prefer to configure various
    | services the application utilizes. Set this in your ".env" file.
    |
    */
    env: <string> 'development',

    /*
    |--------------------------------------------------------------------------
    | Application Debug Mode
    |--------------------------------------------------------------------------
    |
    | When your application is in debug mode, detailed error messages with
    | stack traces will be shown on every error that occurs within your
    | application. If disabled, a simple generic error page is shown.
    |
    */
    debug: <boolean> true,

   /*
    |--------------------------------------------------------------------------
    | Application URL
    |--------------------------------------------------------------------------
    |
    | This URL is used by the console to properly generate URLs when using
    | the Artisan command line tool. You should set this to the root of
    | your application so that it is used when running Artisan tasks.
    |
    */
    url: <string> 'http://localhost',
    asset_url: <string | null> null,

    /*
    |--------------------------------------------------------------------------
    | Autoloaded Service Providers
    |--------------------------------------------------------------------------
    |
    | The service providers listed here will be automatically loaded on the
    | request to your application. Feel free to add your own services to
    | this array to grant expanded functionality to your applications.
    |
    */
    providers: <isProvider[]> [
        /*
         * Arkoren Framework Service Providers...
         */

        /*
         * Package Service Providers...
         */

        /*
         * Application Service Providers...
         */
        AppServiceProvider,
        RouteServiceProvider
    ]

}
